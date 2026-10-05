package cli

import _ "embed"

//go:embed guide.md
var guideText string

func guide() string { return guideText }
