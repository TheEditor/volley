package input

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestACFG03StrictJSON(t *testing.T) {
	for _, text := range []string{`{"x":1,"x":2}`, `{"a":{"x":1,"x":2}}`, `{"a":[{"x":1,"x":2}]}`, `{"x":1} {}`, `{"x":null}`, `null`, `[]`, `true`, `{"x":1,}`, `{"x":}`, "", string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})} {
		t.Run(text, func(t *testing.T) {
			if _, err := Object(strings.NewReader(text)); err == nil {
				t.Fatalf("Accepted %q", text)
			}
		})
	}
	accepted := `{"text":"日本","array":[1,true,""],"a":{}}`
	if _, err := Object(strings.NewReader(accepted)); err != nil {
		t.Fatal(err)
	}
	m, err := Object(strings.NewReader(`{"integer":9007199254740993}`))
	if err != nil || m["integer"] != json.Number("9007199254740993") {
		t.Fatalf("Integer precision: %v %v", m, err)
	}
	duplicateEscape := `{"x":1,"\u0078":2}`
	if _, err := Object(strings.NewReader(duplicateEscape)); err == nil {
		t.Fatal("Escaped duplicate accepted")
	}
	exact := append([]byte(`{"x":"`), bytes.Repeat([]byte{'a'}, Limit-len(`{"x":""}`))...)
	exact = append(exact, []byte(`"}`)...)
	if len(exact) != Limit {
		t.Fatal("Bad fixture length")
	}
	if _, err := Object(bytes.NewReader(exact)); err != nil {
		t.Fatal(err)
	}
	if _, err := Object(bytes.NewReader(append(exact, ' '))); err == nil {
		t.Fatal("Limit+1 accepted")
	}
	nested := `{"a":` + strings.Repeat("[", MaxDepth+1) + "0" + strings.Repeat("]", MaxDepth+1) + "}"
	if _, err := Object(strings.NewReader(nested)); err == nil {
		t.Fatal("Depth limit ignored")
	}
	type Patch struct {
		X int `json:"x"`
	}
	for _, text := range []string{`{"x":"wrong"}`, `{"x":1.5}`, `{"x":true}`, `{"unknown":1}`} {
		var patch Patch
		if err := Decode(strings.NewReader(text), &patch); err == nil {
			t.Fatalf("Typed decode accepted %s", text)
		}
	}
	var patch Patch
	if err := Decode(strings.NewReader(`{"x":8}`), &patch); err != nil || patch.X != 8 {
		t.Fatalf("Typed decode: %v", err)
	}
}
func TestReadStopsAtLimit(t *testing.T) {
	r := &countReader{}
	if _, err := Read(r); err == nil {
		t.Fatal("Infinite input accepted")
	}
	if r.count != Limit+1 {
		t.Fatalf("Read %d bytes", r.count)
	}
}

type countReader struct{ count int }

func (r *countReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = ' '
	}
	r.count += len(b)
	return len(b), nil
}
