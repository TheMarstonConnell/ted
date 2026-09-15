package browser

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestFrameDecoder(t *testing.T) {
	var decoder frameDecoder
	for _, input := range []string{"first frame", "first frame", "second frame", "second frame", "first frame"} {
		got, err := decoder.decode(base64.StdEncoding.EncodeToString([]byte(input)))
		if err != nil || string(got) != input {
			t.Fatalf("frame: %q, %v", got, err)
		}
	}
	if _, err := decoder.decode("broken!"); err == nil {
		t.Fatal("invalid frame accepted")
	}
	got, err := decoder.decode(base64.StdEncoding.EncodeToString([]byte("first frame")))
	if err != nil || string(got) != "first frame" {
		t.Fatal("invalid frame damaged cached content")
	}
}

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
		var decoder frameDecoder
		b.ReportAllocs()
		for b.Loop() {
			if _, err := decoder.decode(encoded); err != nil {
				b.Fatal(err)
			}
		}
	})
}
