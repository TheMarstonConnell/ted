package browser

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	tests := []struct{ input, want string }{
		{"example.com", "http://example.com"},
		{" \t example.com/path?q=one#two \n", "http://example.com/path?q=one#two"},
		{"intranet", "http://intranet"},
		{"localhost:3000/path", "http://localhost:3000/path"},
		{"LOCALHOST:3000", "http://LOCALHOST:3000"},
		{"localhost.:3000", "http://localhost.:3000"},
		{"example.test:8080/?q=a:b", "http://example.test:8080/?q=a:b"},
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"127.0.0.1", "http://127.0.0.1"},
		{"[::1]:8080/path", "http://[::1]:8080/path"},
		{"[2001:db8::1]", "http://[2001:db8::1]"},
		{"::1", "http://[::1]"},
		{"2001:db8::1/path?q=x", "http://[2001:db8::1]/path?q=x"},
		{"fe80::1#part", "http://[fe80::1]#part"},
		{"//example.com/path", "http://example.com/path"},
		{"//[::1]:8080", "http://[::1]:8080"},
		{"https://example.test/a%20b?q=x#y", "https://example.test/a%20b?q=x#y"},
		{" HTTP://example.test ", "HTTP://example.test"},
		{"file:///tmp/example.html", "file:///tmp/example.html"},
		{"about:blank", "about:blank"},
		{"data:text/html,<h1>Hello world</h1>", "data:text/html,<h1>Hello world</h1>"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := normalizeURL(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("normalizeURL(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
			}
			if again, err := normalizeURL(got); err != nil || again != got {
				t.Fatalf("normalization is not idempotent: %q, %v", again, err)
			}
			for _, kind := range []string{"new", "navigate"} {
				data, _ := json.Marshal(LiveCommand{Type: kind, URL: tt.input})
				command, err := ParseLiveCommand(data)
				if err != nil || command.URL != tt.want {
					t.Errorf("ParseLiveCommand(%s) = %+v, %v", data, command, err)
				}
			}
		})
	}
}

func TestNormalizeURLRejectsUnsafeOrMalformedAddresses(t *testing.T) {
	for _, raw := range []string{
		"", " \t\n ", "javascript:alert(1)", " JAVASCRIPT:123 ",
		"vbscript:msgbox(1)", "ftp://example.com", "mailto:a@example.com",
		"custom:123", "chrome://settings", "javascript://example.com",
		"java\nscript:alert(1)", "https://exa\tmple.com", "http://",
		"https:///path", "http:example.com", "/path", "//", "example .com",
		"localhost:abc", "[::1", "example.com:bad", "http://example.com/%xx",
		"[not-ipv6]", "http://[127.0.0.1]", "localhost:65536", "http://::1",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := normalizeURL(raw)
			var se *serviceError
			if got != "" || !errors.As(err, &se) || se.code != "invalid_params" {
				t.Fatalf("normalizeURL(%q) = %q, %v", raw, got, err)
			}
			for _, kind := range []string{"new", "navigate"} {
				data, _ := json.Marshal(map[string]string{"type": kind, "url": raw})
				if _, err := ParseLiveCommand(data); err == nil {
					t.Errorf("ParseLiveCommand accepted %s", data)
				}
			}
		})
	}
}

func TestLiveNewWithoutURLStillOpensBlank(t *testing.T) {
	command, err := ParseLiveCommand([]byte(`{"type":"new"}`))
	if err != nil || command.URL != "" {
		t.Fatalf("new without URL = %+v, %v", command, err)
	}
	if _, err := ParseLiveCommand([]byte(`{"type":"navigate"}`)); err == nil {
		t.Fatal("navigate without URL accepted")
	}
}
