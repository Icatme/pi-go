package codemode

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestWebPImageConfigurationAndPixelBounds(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"empty_vp8", []byte("RIFF\x0c\x00\x00\x00WEBPVP8 \x00\x00\x00\x00")},
		{"empty_vp8l", []byte("RIFF\x0c\x00\x00\x00WEBPVP8L\x00\x00\x00\x00")},
		{"giant_vp8x", webpCanvasForTest(1<<24, 1<<24)},
		{"over_pixel_limit", webpCanvasForTest(8193, 8192)},
		{"truncated_container", []byte("RIFF\xff\xff\xff\x7fWEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")},
		{"trailing_data", append(webpCanvasForTest(1, 1), 0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"mimeType": "image/webp", "data": base64.StdEncoding.EncodeToString(test.data)})
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.Run(context.Background(), `try {image(`+string(raw)+`)} catch(e) {return e.code}`, RunOptions{})
			if err != nil || textOutput(result) != "image_invalid" || len(result.Outputs) != 1 {
				t.Fatalf("invalid WebP admitted: result=%+v err=%v", result, err)
			}
		})
	}
	for _, size := range [][2]uint32{{1, 1}, {8192, 8192}} {
		raw, err := json.Marshal(map[string]string{"mimeType": "image/webp", "data": base64.StdEncoding.EncodeToString(webpCanvasForTest(size[0], size[1]))})
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.Run(context.Background(), `image(`+string(raw)+`)`, RunOptions{})
		if err != nil || len(result.Outputs) != 1 || result.Outputs[0].Type != "image" || result.Outputs[0].MimeType != "image/webp" {
			t.Fatalf("bounded WebP configuration rejected: size=%v result=%+v err=%v", size, result, err)
		}
	}
}

// A VP8X configuration keeps the boundary test independent of compressed pixels.
func webpCanvasForTest(width, height uint32) []byte {
	data := make([]byte, 30)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:], "WEBPVP8X")
	binary.LittleEndian.PutUint32(data[16:20], 10)
	width--
	height--
	for i := range 3 {
		data[24+i] = byte(width >> (8 * i))
		data[27+i] = byte(height >> (8 * i))
	}
	return data
}
