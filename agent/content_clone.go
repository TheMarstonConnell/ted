package agent

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// Clone detaches the opaque JSON backing bytes without re-decoding the content.
func (c Content) Clone() Content {
	c.raw = bytes.Clone(c.raw)
	return c
}

// CloneJSON returns a detached snapshot with its durable JSON representation.
func (c Content) CloneJSON() (Content, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return Content{}, err
	}
	snapshot := Content{raw: raw, text: c.text}
	if !utf8.ValidString(c.text) {
		if err := snapshot.UnmarshalJSON(raw); err != nil {
			return Content{}, err
		}
	}
	return snapshot, nil
}
