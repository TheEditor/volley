// Package prompts supplies shipped source assets to the installed executable.
package prompts

import "embed"

//go:embed *.md profiles/*.md
var Files embed.FS
