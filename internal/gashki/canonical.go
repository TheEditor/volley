package gashki

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// PayloadCanonical encodes decoded public Gashki data using RFC 8785. Owned
// Volley records keep their separate integer-only canonical format.
func PayloadCanonical(v any) ([]byte, error) {
	var out bytes.Buffer
	var write func(any) error
	quote := func(s string) error {
		if !utf8.ValidString(s) {
			return fmt.Errorf("Invalid Unicode string")
		}
		out.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"', '\\':
				out.WriteByte('\\')
				out.WriteRune(r)
			case '\b':
				out.WriteString(`\b`)
			case '\t':
				out.WriteString(`\t`)
			case '\n':
				out.WriteString(`\n`)
			case '\f':
				out.WriteString(`\f`)
			case '\r':
				out.WriteString(`\r`)
			default:
				if r < 32 {
					fmt.Fprintf(&out, `\u%04x`, r)
				} else {
					out.WriteRune(r)
				}
			}
		}
		out.WriteByte('"')
		return nil
	}
	write = func(v any) error {
		switch x := v.(type) {
		case nil:
			out.WriteString("null")
		case bool:
			out.WriteString(strconv.FormatBool(x))
		case string:
			return quote(x)
		case json.Number:
			f, e := strconv.ParseFloat(string(x), 64)
			if e != nil {
				return e
			}
			if f == 0 {
				out.WriteByte('0')
				return nil
			}
			b, e := json.Marshal(f)
			if e != nil {
				return e
			}
			out.Write(b)
		case float64:
			if x == 0 {
				out.WriteByte('0')
				return nil
			}
			b, e := json.Marshal(x)
			if e != nil {
				return e
			}
			out.Write(b)
		case []any:
			out.WriteByte('[')
			for i, e := range x {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := write(e); err != nil {
					return err
				}
			}
			out.WriteByte(']')
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				a, b := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))
				for k := 0; k < len(a) && k < len(b); k++ {
					if a[k] != b[k] {
						return a[k] < b[k]
					}
				}
				return len(a) < len(b)
			})
			out.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := quote(k); err != nil {
					return err
				}
				out.WriteByte(':')
				if err := write(x[k]); err != nil {
					return err
				}
			}
			out.WriteByte('}')
		default:
			return fmt.Errorf("Unsupported public JSON value %T", v)
		}
		return nil
	}
	if err := write(v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// encoding/json substitutes U+FFFD for a lone escaped surrogate. Public
// contract evidence must reject it before decoding or canonical hashing.
func checkedSurrogates(b []byte) error {
	quoted := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return fmt.Errorf("Incomplete JSON escape")
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return fmt.Errorf("Incomplete Unicode escape")
		}
		code, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return e
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return fmt.Errorf("Unpaired low surrogate")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return fmt.Errorf("Unpaired high surrogate")
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("Unpaired high surrogate")
			}
			i += 6
		}
	}
	return nil
}
