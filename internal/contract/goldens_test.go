package contract

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestResponseGoldens(t *testing.T) {
	b, e := os.ReadFile("testdata/goldens.json.gz")
	if e != nil {
		t.Fatal(e)
	}
	indexBytes, e := os.ReadFile("testdata/goldens-index.json")
	if e != nil {
		t.Fatal(e)
	}
	var index struct {
		SHA256   string            `json:"sha256"`
		Examples map[string]string `json:"examples"`
	}
	if e = json.Unmarshal(indexBytes, &index); e != nil {
		t.Fatal(e)
	}
	if HashBytes(b) != index.SHA256 {
		t.Fatal("golden archive pin differs")
	}
	reader, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	raw, e := io.ReadAll(io.LimitReader(reader, 4<<20))
	if e != nil {
		t.Fatal(e)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var examples map[string]any
	if e = decoder.Decode(&examples); e != nil {
		t.Fatal(e)
	}
	r, _ := Load()
	required := map[string]bool{"envelope": true, "error": true, "warning": true, "settings": true, "manifest": true, "receipt": true, "event-page": true, "help": true, "version": true, "final-result": true, "input-receipt": true, "inbox-entry": true, "raw-config-show": true}
	for _, c := range r.Commands {
		required[c.Schema] = true
	}
	for name := range required {
		if _, ok := examples[name]; !ok {
			t.Error("missing response schema/mode golden", name)
		}
	}
	if len(index.Examples) != len(examples) {
		t.Fatal("golden index coverage differs")
	}
	for name, v := range examples {
		t.Run(name, func(t *testing.T) {
			canonical, e := Canonical(v)
			if e != nil {
				t.Fatal(e)
			}
			if index.Examples[name] != HashBytes(canonical) {
				t.Fatal("golden response pin differs")
			}
			if name == "raw-config-show" {
				if !strings.Contains(v.(string), "max_rounds = 5") {
					t.Fatal("raw setting differs")
				}
				return
			}
			if e := Validate(name+".json", v); e != nil {
				t.Fatal(e)
			}
			if name == "envelope" {
				m := v.(map[string]any)
				data, _ := Canonical(m["data"])
				if m["meta"].(map[string]any)["data_hash"] != "sha256:"+HashBytes(data) {
					t.Fatal("golden data hash differs")
				}
			}
		})
	}
}
