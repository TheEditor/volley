package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
)

// Validate normalizes a supplied value according to the single registry.
// File system rules and cross-setting rules belong to the later resolver.
func Validate(s contract.Setting, v any) (any, error) {
	switch s.Type {
	case "boolean":
		if _, ok := v.(bool); !ok {
			return nil, fmt.Errorf("Expected a boolean")
		}
	case "integer":
		var n int64
		switch x := v.(type) {
		case int:
			n = int64(x)
		case int64:
			n = x
		case json.Number:
			var err error
			n, err = strconv.ParseInt(string(x), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("Expected an integer")
			}
		default:
			return nil, fmt.Errorf("Expected an integer")
		}
		for _, bound := range []struct {
			name  string
			lower bool
		}{{"minimum", true}, {"maximum", false}} {
			if raw, exists := s.Validation[bound.name]; exists {
				b, err := strconv.ParseInt(fmt.Sprint(raw), 10, 64)
				if err != nil {
					return nil, err
				}
				if bound.lower && n < b || !bound.lower && n > b {
					return nil, fmt.Errorf("Integer violates %s %d", bound.name, b)
				}
			}
		}
		return n, nil
	case "array":
		var a []string
		switch x := v.(type) {
		case []string:
			a = append([]string{}, x...)
		case []any:
			a = make([]string, 0, len(x))
			for _, e := range x {
				str, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("Expected string array elements")
				}
				a = append(a, str)
			}
		default:
			return nil, fmt.Errorf("Expected a string array")
		}
		if !s.AllowEmpty && len(a) == 0 {
			return nil, fmt.Errorf("Array must not be empty")
		}
		allowed, _ := s.Validation["items"].([]any)
		seen := make(map[string]bool)
		for _, e := range a {
			if seen[e] {
				return nil, fmt.Errorf("Duplicate array item %q", e)
			}
			seen[e] = true
			valid := false
			for _, choice := range allowed {
				if e == choice {
					valid = true
				}
			}
			if !valid {
				return nil, fmt.Errorf("Unsupported array item %q", e)
			}
		}
		return a, nil
	default:
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("Expected a string")
		}
		if !utf8.ValidString(str) || strings.ContainsRune(str, 0) {
			return nil, fmt.Errorf("String contains invalid UTF-8 or NUL")
		}
		if str == "" && !s.AllowEmpty {
			return nil, fmt.Errorf("String must not be empty")
		}
		if choices, ok := s.Validation["enum"].([]any); ok {
			valid := false
			for _, choice := range choices {
				if str == choice {
					valid = true
				}
			}
			if !valid {
				return nil, fmt.Errorf("Unsupported value %q; expected %v", str, choices)
			}
		}
		if s.Validation["identifier"] == true && str != "" {
			if strings.HasPrefix(str, "-") {
				return nil, fmt.Errorf("Identifier must not start with an option")
			}
			for _, ch := range str {
				if unicode.IsControl(ch) {
					return nil, fmt.Errorf("Identifier contains a control character")
				}
			}
		}
		if s.Type == "duration" {
			n, err := time.ParseDuration(str)
			if err != nil {
				return nil, fmt.Errorf("Invalid duration %q", str)
			}
			for _, bound := range []struct {
				name  string
				lower bool
			}{{"minimum", true}, {"maximum", false}} {
				if raw, exists := s.Validation[bound.name]; exists {
					b, err := time.ParseDuration(fmt.Sprint(raw))
					if err != nil {
						return nil, err
					}
					if bound.lower && n < b || !bound.lower && n > b {
						return nil, fmt.Errorf("Duration violates %s %s", bound.name, b)
					}
				}
			}
		}
	}
	return v, nil
}
