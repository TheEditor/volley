package gashkireal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"unicode/utf16"
)

// Public Gashki payload hashes use RFC 8785. This verifier is independent of
// Gashki's internal packages and uses encoding/json's binary64 formatting.
func canonical(v any) []byte {
	var out bytes.Buffer
	var write func(any)
	quote := func(s string) {
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
	}
	write = func(x any) {
		switch x := x.(type) {
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
				quote(k)
				out.WriteByte(':')
				write(x[k])
			}
			out.WriteByte('}')
		case []any:
			out.WriteByte('[')
			for i, e := range x {
				if i > 0 {
					out.WriteByte(',')
				}
				write(e)
			}
			out.WriteByte(']')
		case string:
			quote(x)
		case json.Number:
			f, err := strconv.ParseFloat(string(x), 64)
			if err != nil {
				panic(err)
			}
			if f == 0 {
				out.WriteByte('0')
				return
			}
			b, err := json.Marshal(f)
			if err != nil {
				panic(err)
			}
			out.Write(b)
		default:
			b, err := json.Marshal(x)
			if err != nil {
				panic(err)
			}
			out.Write(b)
		}
	}
	write(v)
	return out.Bytes()
}

func TestCanonicalVerifier(t *testing.T) {
	if got := string(canonical(map[string]any{"\ue000": json.Number("-0"), "\U0001f600": json.Number("1e-7"), "\r": "\u2028<>&\x00"})); got != "{\"\\r\":\"\u2028<>&\\u0000\",\"😀\":1e-7,\"\ue000\":0}" {
		t.Fatal(got)
	}
}
