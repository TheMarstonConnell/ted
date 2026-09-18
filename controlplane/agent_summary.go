package controlplane

import (
	"strings"
	"unicode"
)

func cloneAgentSnapshot(a Agent, summary bool) Agent {
	if summary {
		if a.Title == "" && len(a.Queue) > 0 {
			a.DisplayTitle = titlePreview(a.Queue[0].Text)
			if a.DisplayTitle == "" && len(a.Queue[0].Attachments) > 0 {
				a.DisplayTitle = titlePreview(a.Queue[0].Attachments[0].Name)
			}
		}
		a.Queue, a.Messages, a.Events = nil, nil, nil
	}
	return cloneAgent(a)
}

// Stop at the preview limit without allocating a normalized copy of a large prompt.
func titlePreview(text string) string {
	var b strings.Builder
	count, space := 0, false
	for _, r := range text {
		if unicode.IsSpace(r) || r == '\uFEFF' {
			space = count > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			count++
			space = false
		}
		if count == 80 {
			break
		}
		b.WriteRune(r)
		count++
		if count == 80 {
			break
		}
	}
	return b.String()
}
