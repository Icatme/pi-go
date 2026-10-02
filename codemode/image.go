package codemode

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	_ "golang.org/x/image/webp"
)

func (r *runState) image(raw string) string {
	if len(raw) > r.outputLimit*2 {
		return fail("output_limit", "image exceeds output byte limit")
	}
	var value struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
		ImageURL string `json:"image_url"`
	}
	var url string
	if err := json.Unmarshal([]byte(raw), &url); err != nil {
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return fail("image_invalid", "expected data URL or image object")
		}
		url = value.ImageURL
	}
	if url != "" {
		header, data, ok := strings.Cut(url, ",")
		if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
			return fail("image_invalid", "image URL must be an inline base64 data URL")
		}
		value.MimeType = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		value.Data = data
	}
	if value.Type != "" && value.Type != "image" {
		return fail("image_invalid", "unsupported image type")
	}
	if len(value.Data) > r.outputLimit-r.outputBytes {
		return fail("output_limit", "image exceeds remaining output byte limit")
	}
	switch value.MimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return fail("image_invalid", "unsupported image MIME")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(value.Data)
	if err != nil || len(data) == 0 {
		return fail("image_invalid", "invalid image base64")
	}
	if value.MimeType == "image/webp" {
		if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || int(binary.LittleEndian.Uint32(data[4:8])) != len(data)-8 || (string(data[12:16]) != "VP8 " && string(data[12:16]) != "VP8L" && string(data[12:16]) != "VP8X") {
			return fail("image_invalid", "MIME does not match WebP data")
		}
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	expected := strings.TrimPrefix(value.MimeType, "image/")
	if err != nil || format != expected || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 64<<20 {
		return fail("image_invalid", "MIME does not match supported image data")
	}
	return r.appendOutput(Output{Type: "image", MimeType: value.MimeType, Data: value.Data}, len(value.Data))
}
