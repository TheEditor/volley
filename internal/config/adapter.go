package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"golang.org/x/sys/unix"
)

type Invalid struct {
	File, Key, Message string
	Line, Column       int
}

func (e *Invalid) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", e.File, e.Line, e.Column, e.Key, e.Message)
}

type ReadFailure struct {
	Path  string
	Cause error
}

func (e *ReadFailure) Error() string {
	return fmt.Sprintf("Cannot read config %s: %v", e.Path, e.Cause)
}
func (e *ReadFailure) Unwrap() error { return e.Cause }

type Span struct{ Start, End, Line, Column int }
type Document struct {
	File   string
	Bytes  []byte
	Values map[string]any
	Spans  map[string]Span
}

// ReadFile allows a read-only symlink, but verifies the opened descriptor is
// regular. Nonblocking open prevents a raced FIFO from waiting for a writer.
func ReadFile(path string) (*Document, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, &ReadFailure{path, err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, &ReadFailure{path, err}
	}
	if !st.Mode().IsRegular() {
		return nil, &ReadFailure{path, fmt.Errorf("Config input is not a regular file")}
	}
	b, err := input.Read(f)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		return nil, &ReadFailure{path, err}
	}
	return Parse(path, b)
}
func Parse(file string, b []byte) (*Document, error) {
	if len(b) > input.Limit || !utf8.Valid(b) {
		return nil, &Invalid{file, "", "Input exceeds 1 MiB or is not valid UTF-8", 1, 1}
	}
	r, err := contract.Load()
	if err != nil {
		return nil, err
	}
	declarations := make(map[string]contract.Setting)
	for _, s := range r.Settings {
		declarations[s.Key] = s
	}
	d := &Document{file, bytes.Clone(b), make(map[string]any), make(map[string]Span)}
	p := unstable.Parser{}
	p.Reset(b)
	for p.NextExpression() {
		node := p.Expression()
		keys := node.Key()
		keys.Next()
		keyNode := keys.Node()
		key := string(keyNode.Data)
		shape := p.Shape(keyNode.Raw)
		line, column := shape.Start.Line, shape.Start.Column
		invalid := func(msg string) error { return &Invalid{file, key, msg, line, column} }
		if node.Kind != unstable.KeyValue {
			return nil, invalid("Tables are not accepted")
		}
		if !keys.IsLast() {
			return nil, invalid("Dotted keys are not accepted")
		}
		rawKey := p.Raw(keyNode.Raw)
		if len(rawKey) == 0 || rawKey[0] == '\'' || rawKey[0] == '"' {
			return nil, invalid("Quoted keys are not accepted")
		}
		if _, ok := declarations[key]; !ok {
			return nil, invalid("Unknown setting")
		}
		if _, ok := d.Spans[key]; ok {
			return nil, invalid("Duplicate setting")
		}
		// The library's KeyValue range ends immediately after its complete value,
		// including multiline arrays. Skip only the separator after its key range.
		start := int(keyNode.Raw.Offset + keyNode.Raw.Length)
		end := int(node.Raw.Offset + node.Raw.Length)
		for start < end && (b[start] == ' ' || b[start] == '\t') {
			start++
		}
		if start >= end || b[start] != '=' {
			return nil, invalid("Invalid library value range")
		}
		start++
		for start < end && (b[start] == ' ' || b[start] == '\t') {
			start++
		}
		if start >= end {
			return nil, invalid("Empty library value range")
		}
		d.Spans[key] = Span{start, end, line, column}
	}
	if err := p.Error(); err != nil {
		// Obtain the stable library diagnostic with its source position.
		var ignored map[string]any
		stable := toml.Unmarshal(b, &ignored)
		return nil, diagnostic(file, stable)
	}
	var values map[string]any
	if err := toml.Unmarshal(b, &values); err != nil {
		return nil, diagnostic(file, err)
	}
	for _, s := range r.Settings {
		v, ok := values[s.Key]
		if !ok {
			continue
		}
		v, err := Validate(s, v)
		if err != nil {
			span := d.Spans[s.Key]
			return nil, &Invalid{file, s.Key, err.Error(), span.Line, span.Column}
		}
		d.Values[s.Key] = v
	}
	return d, nil
}
func diagnostic(file string, err error) error {
	if err == nil {
		return &Invalid{file, "", "Invalid TOML", 1, 1}
	}
	var decode *toml.DecodeError
	if errors.As(err, &decode) {
		line, column := decode.Position()
		return &Invalid{file, strings.Join(decode.Key(), "."), decode.Error(), line, column}
	}
	return &Invalid{file, "", err.Error(), 1, 1}
}

// Replace is a pure candidate operation. It neither writes nor trusts a
// previously parsed mutable document. A failed edit returns no candidate.
func (d *Document) Replace(updates map[string]any) ([]byte, bool, error) {
	original, err := Parse(d.File, d.Bytes)
	if err != nil {
		return nil, false, err
	}
	r, err := contract.Load()
	if err != nil {
		return nil, false, err
	}
	known := make(map[string]bool)
	for _, s := range r.Settings {
		known[s.Key] = true
	}
	for k := range updates {
		if !known[k] {
			return nil, false, &Invalid{d.File, k, "Unknown setting", 1, 1}
		}
	}
	type edit struct {
		start, end int
		b          []byte
	}
	edits := make([]edit, 0)
	var appended bytes.Buffer
	newline := "\n"
	if bytes.Contains(d.Bytes, []byte("\r\n")) {
		newline = "\r\n"
	}
	for _, s := range r.Settings {
		value, ok := updates[s.Key]
		if !ok {
			continue
		}
		value, err = Validate(s, value)
		if err != nil {
			return nil, false, &Invalid{d.File, s.Key, err.Error(), 1, 1}
		}
		if old, exists := original.Values[s.Key]; exists && reflect.DeepEqual(old, value) {
			continue
		}
		encoded, err := toml.Marshal(map[string]any{s.Key: value})
		if err != nil {
			return nil, false, err
		}
		rendered, err := Parse(d.File, encoded)
		if err != nil {
			return nil, false, err
		}
		span := rendered.Spans[s.Key]
		val := encoded[span.Start:span.End]
		if span, exists := original.Spans[s.Key]; exists {
			edits = append(edits, edit{span.Start, span.End, val})
		} else {
			appended.WriteString(s.Key + " = ")
			appended.Write(val)
			appended.WriteString(newline)
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	out := bytes.Clone(original.Bytes)
	for _, e := range edits {
		out = append(append(append([]byte{}, out[:e.start]...), e.b...), out[e.end:]...)
	}
	if appended.Len() > 0 {
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, []byte(newline)...)
		}
		out = append(out, appended.Bytes()...)
	}
	if _, err := Parse(d.File, out); err != nil {
		return nil, false, err
	}
	return out, !bytes.Equal(out, original.Bytes), nil
}
