package agent

import _ "embed"

// SYSTEM_PROMPT is embedded at build time. Edit system_prompt.md and rebuild
// to change the agent's instructions.
//
//go:embed system_prompt.md
var SYSTEM_PROMPT string
