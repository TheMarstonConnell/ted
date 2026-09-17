package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestValidateAttachments(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for _, format := range []string{"png", "jpeg", "gif", "webp"} {
		t.Run(format, func(t *testing.T) {
			var b bytes.Buffer
			switch format {
			case "png":
				_ = png.Encode(&b, img)
			case "jpeg":
				_ = jpeg.Encode(&b, img, nil)
			case "gif":
				_ = gif.Encode(&b, img, nil)
			case "webp":
				data, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
				if err != nil {
					t.Fatal(err)
				}
				b.Write(data)
			}
			a := Attachment{Name: "screen." + format, URL: "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(b.Bytes())}
			if err := ValidateAttachments([]Attachment{a}); err != nil {
				t.Fatal(err)
			}
			a.URL = strings.Replace(a.URL, "image/"+format, "image/svg+xml", 1)
			if err := ValidateAttachments([]Attachment{a}); err == nil {
				t.Fatal("accepted unsupported MIME")
			}
		})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	valid := "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
	huge := bytes.Clone(b.Bytes())
	binary.BigEndian.PutUint32(huge[16:20], MaxAttachmentDimension+1)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	pixelBomb := bytes.Clone(b.Bytes())
	binary.BigEndian.PutUint32(pixelBomb[16:20], 8192)
	binary.BigEndian.PutUint32(pixelBomb[20:24], 8192)
	binary.BigEndian.PutUint32(pixelBomb[29:33], crc32.ChecksumIEEE(pixelBomb[12:29]))
	tests := map[string][]Attachment{
		"remote":        {{URL: "https://example.com/image.png"}},
		"file":          {{URL: "file:///tmp/image.png"}},
		"mime mismatch": {{URL: strings.Replace(valid, "image/png", "image/jpeg", 1)}},
		"bad base64":    {{URL: "data:image/png;base64,!!!"}},
		"not image":     {{URL: "data:image/png;base64,aGVsbG8="}},
		"truncated":     {{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()[:33])}},
		"dimensions":    {{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(huge)}},
		"pixels":        {{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixelBomb)}},
		"size":          {{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxAttachmentBytes+1))}},
		"count":         make([]Attachment, 5),
		"name":          {{Name: strings.Repeat("界", 257), URL: valid}},
	}
	for name, attachments := range tests {
		t.Run(name, func(t *testing.T) {
			if ValidateAttachments(attachments) == nil {
				t.Fatal("accepted invalid attachments")
			}
		})
	}
	boundary := Attachment{Name: strings.Repeat("界", 256), URL: valid}
	if err := ValidateAttachments([]Attachment{boundary, boundary, boundary, boundary}); err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentByteBoundary(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, MaxAttachmentBytes)
	copy(data, b.Bytes())
	a := Attachment{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}
	if err := ValidateAttachments([]Attachment{a, a, a, a}); err != nil {
		t.Fatalf("20 MiB total rejected: %v", err)
	}
	a.URL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(append(data, 0))
	if err := ValidateAttachments([]Attachment{a}); err == nil {
		t.Fatal("accepted 5 MiB + 1")
	}
}

func TestAttachmentRejectsWebPCanvasFrameMismatch(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	extended := append([]byte(nil), data[:12]...)
	// VP8X claims 2x1, while its VP8 frame is 1x1.
	extended = append(extended, []byte{'V', 'P', '8', 'X', 10, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0}...)
	extended = append(extended, data[12:]...)
	binary.LittleEndian.PutUint32(extended[4:8], uint32(len(extended)-8))
	if err := ValidateAttachments([]Attachment{{URL: "data:image/webp;base64," + base64.StdEncoding.EncodeToString(extended)}}); err == nil {
		t.Fatal("accepted mismatched WebP canvas/frame")
	}
}

func TestStandaloneTurnRejectsInvalidAttachments(t *testing.T) {
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	validURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes.Bytes())
	for _, tt := range []struct{ name, kind, url string }{
		{"remote URL", "user", "https://example.com/private.png"},
		{"invalid bytes", "user", "data:image/png;base64,aGVsbG8="},
		{"bot image", "bot", validURL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := settingsProvider()
			p.complete = func(CompletionRequest) (*Response, error) {
				t.Fatal("invalid attachment reached the provider")
				return nil, nil
			}
			a := NewAgent(nil, []Provider{p})
			before := len(a.Messages())
			err := a.TurnMessageAttachmentsContext(context.Background(), "inspect", tt.kind, "", []Attachment{{Name: "screen.png", URL: tt.url}})
			if err == nil || len(a.Messages()) != before {
				t.Fatalf("invalid input changed conversation or succeeded: %v", err)
			}
		})
	}
}
