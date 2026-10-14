package pigo

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"strings"
)

// These are local decoding limits, not advertised provider upload limits.
const (
	commandCodeMaxPNGBytes      = 32 << 20
	commandCodeMaxPNGPixelCount = 64 << 20
)

func commandCodePrepareImage(model Model, content ImageContent) (ImageContent, error) {
	if _, err := commandCodeImage(content); err != nil {
		return ImageContent{}, err
	}
	// Command Code's GPT-6 Luna misreads PNG inputs on both native and
	// generate routes. Prepare JPEG once, before routing, without changing
	// caller history. Other models and image formats keep their original data.
	if model.ID != "gpt-6-luna" || !strings.EqualFold(strings.TrimSpace(content.MIMEType), "image/png") {
		return content, nil
	}
	if len(content.Data) > base64.StdEncoding.EncodedLen(commandCodeMaxPNGBytes) {
		return ImageContent{}, fmt.Errorf("Command Code GPT-6 Luna PNG exceeds the 32 MiB conversion limit")
	}
	data, err := base64.StdEncoding.DecodeString(content.Data)
	if err != nil {
		return ImageContent{}, fmt.Errorf("Command Code GPT-6 Luna PNG base64: %w", err)
	}
	if len(data) > commandCodeMaxPNGBytes {
		return ImageContent{}, fmt.Errorf("Command Code GPT-6 Luna PNG exceeds the 32 MiB conversion limit")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ImageContent{}, fmt.Errorf("Command Code GPT-6 Luna PNG header: %w", err)
	}
	// Match codemode's pixel ceiling, and JPEG's maximum dimension. Check
	// before decoding pixels so a small compressed PNG cannot exhaust memory.
	if config.Width <= 0 || config.Height <= 0 || config.Width >= 1<<16 || config.Height >= 1<<16 || int64(config.Width)*int64(config.Height) > commandCodeMaxPNGPixelCount {
		return ImageContent{}, fmt.Errorf("Command Code GPT-6 Luna PNG dimensions exceed JPEG conversion limits (65535 per side, 64 Mi pixels)")
	}
	source, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return ImageContent{}, fmt.Errorf("Command Code GPT-6 Luna PNG decode: %w", err)
	}
	canvas := image.NewRGBA(source.Bounds())
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), source, source.Bounds().Min, draw.Over)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 95}); err != nil {
		return ImageContent{}, fmt.Errorf("Command Code GPT-6 Luna PNG to JPEG: %w", err)
	}
	content.MIMEType = "image/jpeg"
	content.Data = base64.StdEncoding.EncodeToString(encoded.Bytes())
	return content, nil
}
