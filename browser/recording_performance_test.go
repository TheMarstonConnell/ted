package browser

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func BenchmarkRepeatedFrame(b *testing.B) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1, 2, 3, 4}, 64<<10))
	b.Run("decode_every_tick", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cache_unchanged_frame", func(b *testing.B) {
		var cachedEncoded string
		var cachedData []byte
		b.ReportAllocs()
		for b.Loop() {
			if encoded != cachedEncoded {
				data, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					b.Fatal(err)
				}
				cachedEncoded, cachedData = encoded, data
			}
		}
		_ = cachedData
	})
}
