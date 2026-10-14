package pigo

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func commandCodePNGFixture(t *testing.T) ImageContent {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 48, 16))
	draw.Draw(img, image.Rect(0, 0, 16, 16), image.NewUniform(color.NRGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(16, 0, 32, 16), image.NewUniform(color.NRGBA{B: 255, A: 128}), image.Point{}, draw.Src)
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return ImageContent{MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(data.Bytes())}
}

func TestCommandCodeLunaPNGContext(t *testing.T) {
	original := commandCodePNGFixture(t)
	ctx := Context{Messages: []Message{
		UserMessage{Content: []ContentBlock{TextContent{Text: "inspect"}, original}},
		ToolResultMessage{ToolCallID: "call", Content: []ContentBlock{original}},
	}}
	for _, id := range []string{"gpt-6-luna", "claude-haiku-5-5", "deepseek/deepseek-v4.1-flash", "gpt-5.6-luna"} {
		t.Run(id, func(t *testing.T) {
			model := newCommandCodeModel(id, id, 100000, 4096, UsageCost{})
			out, err := commandCodeImageContext(model, ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range []ImageContent{out.Messages[0].(UserMessage).Content.([]ContentBlock)[1].(ImageContent), out.Messages[1].(ToolResultMessage).Content[0].(ImageContent)} {
				if id != "gpt-6-luna" {
					if got != original {
						t.Fatal("unaffected model image changed")
					}
					continue
				}
				if got.MIMEType != "image/jpeg" || got.Data == original.Data {
					t.Fatal("Luna PNG was not converted to JPEG")
				}
				data, err := base64.StdEncoding.DecodeString(got.Data)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := jpeg.Decode(bytes.NewReader(data))
				if err != nil || decoded.Bounds() != image.Rect(0, 0, 48, 16) {
					t.Fatalf("JPEG dimensions changed or invalid: %v", err)
				}
				for i, want := range []color.RGBA{{255, 0, 0, 255}, {127, 127, 255, 255}, {255, 255, 255, 255}} {
					got := color.RGBAModel.Convert(decoded.At(i*16+8, 8)).(color.RGBA)
					if math.Abs(float64(got.R)-float64(want.R)) > 4 || math.Abs(float64(got.G)-float64(want.G)) > 4 || math.Abs(float64(got.B)-float64(want.B)) > 4 {
						t.Fatalf("JPEG color/white compositing incorrect: got=%v want=%v", got, want)
					}
				}
			}
			again, err := commandCodeImageContext(model, out)
			if err != nil || !reflect.DeepEqual(out, again) {
				t.Fatalf("image preparation is not idempotent: %v", err)
			}
			if ctx.Messages[0].(UserMessage).Content.([]ContentBlock)[1] != original || ctx.Messages[1].(ToolResultMessage).Content[0] != original {
				t.Fatal("caller images mutated")
			}
		})
	}
}

func TestCommandCodeLunaInvalidPNG(t *testing.T) {
	valid := commandCodePNGFixture(t)
	large, _ := base64.StdEncoding.DecodeString(valid.Data)
	// A valid IHDR CRC with huge dimensions must be rejected before decoding pixels.
	binary.BigEndian.PutUint32(large[16:20], 100000)
	binary.BigEndian.PutUint32(large[20:24], 100000)
	binary.BigEndian.PutUint32(large[29:33], crc32.ChecksumIEEE(large[12:29]))
	pixels := bytes.Clone(large)
	binary.BigEndian.PutUint32(pixels[16:20], 8193)
	binary.BigEndian.PutUint32(pixels[20:24], 8193)
	binary.BigEndian.PutUint32(pixels[29:33], crc32.ChecksumIEEE(pixels[12:29]))
	for name, data := range map[string]string{
		"base64":     "!invalid!",
		"not PNG":    base64.StdEncoding.EncodeToString([]byte("not an image")),
		"truncated":  valid.Data[:len(valid.Data)/2],
		"dimensions": base64.StdEncoding.EncodeToString(large),
		"pixels":     base64.StdEncoding.EncodeToString(pixels),
	} {
		t.Run(name, func(t *testing.T) {
			model := newCommandCodeModel("gpt-6-luna", "Luna", 100000, 4096, UsageCost{})
			_, err := commandCodeImageContext(model, Context{Messages: []Message{UserMessage{Content: []ContentBlock{ImageContent{MIMEType: "image/png", Data: data}}}}})
			if err == nil || !strings.Contains(err.Error(), "PNG") {
				t.Fatalf("expected explicit PNG preparation error, got %v", err)
			}
			if (name == "dimensions" || name == "pixels") && !strings.Contains(err.Error(), "dimensions exceed") {
				t.Fatalf("dimensions must be checked before pixel decoding: %v", err)
			}
		})
	}
}

func TestCommandCodeLunaPNGRouting(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-completions", "commandcode-custom"} {
		t.Run(string(api), func(t *testing.T) {
			original := commandCodePNGFixture(t)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				body, _ := io.ReadAll(r.Body)
				if !bytes.Contains(body, []byte("data:image/jpeg;base64,")) || bytes.Contains(body, []byte(original.Data)) || bytes.Contains(body, []byte("image/png")) {
					t.Error("wire payload must contain converted JPEG only")
				}
				if api == "commandcode-custom" && r.URL.Path != "/alpha/generate" {
					w.WriteHeader(http.StatusForbidden)
					io.WriteString(w, `{"error":{"code":"upgrade_required"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch api {
				case "openai-responses":
					io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_image\",\"status\":\"completed\",\"output\":[]}}\n\n")
				case "openai-completions":
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				default:
					io.WriteString(w, "{\"type\":\"finish\",\"finishReason\":\"stop\"}\n")
				}
			}))
			defer server.Close()
			model := newCommandCodeModel("gpt-6-luna", "Luna", 100000, 4096, UsageCost{})
			model.BaseURL = server.URL
			native := api
			if native == "commandcode-custom" {
				native = "openai-responses"
			}
			model.Compat = &CommandCodeCompat{API: native}
			ctx := Context{Messages: []Message{UserMessage{Content: []ContentBlock{original}}}}
			before, _ := json.Marshal(ctx)
			result := CompleteSimple(model, ctx, SimpleStreamOptions{APIKey: "image-test-" + string(api)})
			if result.StopReason != StopReasonStop || result.API != api {
				t.Fatalf("image request failed: %+v", result)
			}
			wantRequests := 1
			if api == "commandcode-custom" {
				wantRequests = 2
			}
			if requests != wantRequests {
				t.Fatalf("requests=%d want=%d", requests, wantRequests)
			}
			after, _ := json.Marshal(ctx)
			if !bytes.Equal(before, after) {
				t.Fatal("caller image changed after request")
			}
			result = CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: []ContentBlock{ImageContent{MIMEType: "image/png", Data: "invalid"}}}}}, SimpleStreamOptions{APIKey: "image-test-" + string(api)})
			if result.StopReason != StopReasonError || requests != wantRequests {
				t.Fatal("invalid PNG must fail without an HTTP request")
			}
		})
	}
}
