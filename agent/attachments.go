package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/riff"
	"golang.org/x/image/vp8"
	"golang.org/x/image/vp8l"
	_ "golang.org/x/image/webp"
)

const (
	MaxAttachments         = 4
	MaxAttachmentBytes     = 5 << 20
	MaxAttachmentDimension = 16384
	MaxAttachmentPixels    = 16 << 20
)

// Attachment is an inline image, never a filesystem path or remote URL.
type Attachment struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ValidateAttachments bounds allocations before decoding image pixels.
func ValidateAttachments(attachments []Attachment) error {
	if len(attachments) > MaxAttachments {
		return fmt.Errorf("at most %d attachments are allowed", MaxAttachments)
	}
	for _, attachment := range attachments {
		if !utf8.ValidString(attachment.Name) || utf8.RuneCountInString(attachment.Name) > 256 {
			return fmt.Errorf("attachment name must not exceed 256 characters")
		}
		header, encoded, ok := strings.Cut(attachment.URL, ",")
		formats := map[string]string{"data:image/png;base64": "png", "data:image/jpeg;base64": "jpeg", "data:image/webp;base64": "webp", "data:image/gif;base64": "gif"}
		format, supported := formats[header]
		if !ok || !supported {
			return fmt.Errorf("attachment must be an inline base64 PNG, JPEG, WebP or GIF data URL")
		}
		if len(encoded) > base64.StdEncoding.EncodedLen(MaxAttachmentBytes) {
			return fmt.Errorf("attachment exceeds 5 MiB")
		}
		if strings.ContainsAny(encoded, "\r\n") {
			return fmt.Errorf("attachment base64 must not contain whitespace")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("invalid attachment base64")
		}
		if len(data) > MaxAttachmentBytes {
			return fmt.Errorf("attachment exceeds 5 MiB")
		}
		config, actual, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || actual != format {
			return fmt.Errorf("attachment image bytes do not match MIME type")
		}
		if config.Width <= 0 || config.Height <= 0 || config.Width > MaxAttachmentDimension || config.Height > MaxAttachmentDimension || int64(config.Width)*int64(config.Height) > MaxAttachmentPixels {
			return fmt.Errorf("attachment dimensions exceed 16384 per side or 16777216 pixels")
		}
		if format == "webp" {
			if err := validateWebPFrame(data, config); err != nil {
				return err
			}
		}
		if _, actual, err = image.Decode(bytes.NewReader(data)); err != nil || actual != format {
			return fmt.Errorf("invalid attachment image data")
		}
	}
	return nil
}

func attachmentContent(text string, attachments []Attachment) Content {
	if len(attachments) == 0 {
		return TextContent(text)
	}
	parts := make([]map[string]any, 0, len(attachments)+1)
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, attachment := range attachments {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": attachment.URL}})
	}
	raw, _ := json.Marshal(parts)
	return Content{raw: raw, text: text}
}

// Extended WebP canvas dimensions can differ from the allocating frame header.
func validateWebPFrame(data []byte, canvas image.Config) error {
	_, reader, err := riff.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid WebP container")
	}
	for {
		id, size, chunk, err := reader.Next()
		if err != nil {
			return fmt.Errorf("missing WebP image frame")
		}
		var width, height int
		switch string(id[:]) {
		case "VP8 ":
			if size > MaxAttachmentBytes {
				return fmt.Errorf("invalid WebP frame size")
			}
			decoder := vp8.NewDecoder()
			decoder.Init(chunk, int(size))
			header, err := decoder.DecodeFrameHeader()
			if err != nil {
				return fmt.Errorf("invalid WebP frame header")
			}
			width, height = header.Width, header.Height
		case "VP8L":
			config, err := vp8l.DecodeConfig(chunk)
			if err != nil {
				return fmt.Errorf("invalid WebP frame header")
			}
			width, height = config.Width, config.Height
		default:
			if _, err := io.Copy(io.Discard, chunk); err != nil {
				return fmt.Errorf("invalid WebP chunk")
			}
			continue
		}
		if width != canvas.Width || height != canvas.Height {
			return fmt.Errorf("WebP frame dimensions do not match canvas")
		}
		return nil
	}
}
