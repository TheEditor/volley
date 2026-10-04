// Package input reads bounded, strict user input before any mutation.
package input

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

const Limit = 1 << 20
const MaxDepth = 128

// Read accepts the exact limit and reads at most one extra byte.
func Read(r io.Reader) ([]byte, error) { return read(r, Limit) }
func read(r io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, fmt.Errorf("Input exceeds %d bytes", limit)
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("Input is not valid UTF-8")
	}
	return b, nil
}

// JSON rejects repeated names at every depth, nulls, trailing values, and
// excessive nesting. UseNumber keeps integer precision for typed validation.
func JSON(r io.Reader) (any, error) {
	return decode(r, Limit, false)
}

// Record parses owned records with schema-declared nulls and a caller limit.
// User input surfaces use JSON or Object, which reject null.
func Record(r io.Reader, limit int) (any, error) { return decode(r, limit, true) }
func decode(r io.Reader, limit int, nullable bool) (any, error) {
	b, err := read(r, limit)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := value(d, 0, nullable)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("Trailing JSON data")
	}
	return v, nil
}
func value(d *json.Decoder, depth int, nullable bool) (any, error) {
	if depth > MaxDepth {
		return nil, fmt.Errorf("JSON nesting exceeds %d", MaxDepth)
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if t == nil && !nullable {
		return nil, fmt.Errorf("JSON null is not accepted")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		m := make(map[string]any)
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("Expected a JSON object key")
			}
			if _, exists := m[key]; exists {
				return nil, fmt.Errorf("Duplicate JSON key %q", key)
			}
			child, err := value(d, depth+1, nullable)
			if err != nil {
				return nil, err
			}
			m[key] = child
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("Unclosed JSON object")
		}
		return m, nil
	case '[':
		a := make([]any, 0)
		for d.More() {
			child, err := value(d, depth+1, nullable)
			if err != nil {
				return nil, err
			}
			a = append(a, child)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("Unclosed JSON array")
		}
		return a, nil
	default:
		return nil, fmt.Errorf("Unexpected JSON delimiter")
	}
}
func Object(r io.Reader) (map[string]any, error) {
	v, err := JSON(r)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Expected a JSON object")
	}
	return m, nil
}

// Decode adds closed typed-field validation after the strict grammar check.
func Decode(r io.Reader, target any) error {
	v, err := Object(r)
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
