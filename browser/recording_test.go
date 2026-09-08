package browser

import (
	"strings"
	"testing"
)

func TestEncoderOutputIsBounded(t *testing.T) {
	var output encoderOutput
	for i := 0; i < 20; i++ {
		data := strings.Repeat("x", 1024)
		n, err := output.Write([]byte(data))
		if n != len(data) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if output.Len() != 8192 {
		t.Fatalf("retained %d bytes", output.Len())
	}
}
