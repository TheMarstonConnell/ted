package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMarkdownCodeIsInverted(t *testing.T) {
	for _, input := range []string{
		"Use `hello_code` here.",
		"```\nhello_code\n```",
		"```go\nvar hello_code = 1\n```",
	} {
		t.Run(input, func(t *testing.T) {
			renderer, err := newMarkdownRenderer(80)
			if err != nil {
				t.Fatal(err)
			}
			out, err := renderer.Render(input)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(ansi.Strip(out), "hello_code") {
				t.Fatalf("code content missing: %q", out)
			}
			if !strings.Contains(out, "\x1b[7m") && !strings.Contains(out, ";7m") && !strings.Contains(out, "[7;") {
				t.Fatalf("expected reverse-video code: %q", out)
			}
			if strings.Contains(out, "38;5;203") || strings.Contains(out, "48;5;236") {
				t.Fatalf("unexpected old code colors: %q", out)
			}
		})
	}
}
