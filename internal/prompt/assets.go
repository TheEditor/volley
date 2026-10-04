// Package prompt owns typed rendering and source/rendered asset hashes.
package prompt

import "github.com/TheEditor/volley/prompts"

func Template(name string) ([]byte, error) { return prompts.Files.ReadFile(name) }
