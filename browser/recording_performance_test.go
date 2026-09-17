package browser

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestFrameDecoder(t *testing.T) {
	var decoder frameDecoder
	firstEncoded := base64.StdEncoding.EncodeToString([]byte("first frame"))
	first, err := decoder.decode(firstEncoded)
	if err != nil || string(first) != "first frame" {
		t.Fatalf("first frame: %q, %v", first, err)
	}
	repeated, err := decoder.decode(firstEncoded)
	if err != nil || &repeated[0] != &first[0] {
		t.Fatal("unchanged frame was decoded again")
	}

	secondEncoded := base64.StdEncoding.EncodeToString([]byte("second frame"))
	second, err := decoder.decode(secondEncoded)
	if err != nil || string(second) != "second frame" {
		t.Fatalf("changed frame: %q, %v", second, err)
	}
	if _, err := decoder.decode("broken!"); err == nil {
		t.Fatal("invalid frame accepted")
	}
	repeated, err = decoder.decode(secondEncoded)
	if err != nil || &repeated[0] != &second[0] {
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
