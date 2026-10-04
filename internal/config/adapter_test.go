package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/TheEditor/volley/internal/input"
)

func TestACFG01Grammar(t *testing.T) {
	fixtures := []struct {
		name, text string
		values     map[string]any
		lines      map[string]int
	}{
		{"empty", "", map[string]any{}, map[string]int{}},
		{"CRLF", "# header\r\nmax_rounds = 8 # count\r\npersistent = true\r\n", map[string]any{"max_rounds": int64(8), "persistent": true}, map[string]int{"max_rounds": 2, "persistent": 3}},
		{"Unicode", "context_dir = 'référence/日本'\n", map[string]any{"context_dir": "référence/日本"}, map[string]int{"context_dir": 1}},
		{"escapes", "context_dir = \"a\\tb\\u263A\\\"c\" # keep\n", map[string]any{"context_dir": "a\tb☺\"c"}, map[string]int{"context_dir": 1}},
		{"multiline basic", "# before\ncontext_dir = \"\"\"\nréférence\nsecond line\"\"\" # suffix\nmax_rounds=8\n", map[string]any{"context_dir": "référence\nsecond line", "max_rounds": int64(8)}, map[string]int{"context_dir": 2, "max_rounds": 5}},
		{"multiline literal", "context_dir = '''\nC:\\raw\n日本'''\n", map[string]any{"context_dir": "C:\\raw\n日本"}, map[string]int{"context_dir": 1}},
		{"array", "claude_planner_tools = [\n 'Read', # one\n \"Write\",\n 'Skill',\n] # suffix\nmax_rounds=0x8\n", map[string]any{"claude_planner_tools": []string{"Read", "Write", "Skill"}, "max_rounds": int64(8)}, map[string]int{"claude_planner_tools": 1, "max_rounds": 6}},
	}
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			d, err := Parse("settings.toml", []byte(f.text))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(d.Values, f.values) {
				t.Fatalf("Values: %#v", d.Values)
			}
			for key, line := range f.lines {
				if d.Spans[key].Line != line {
					t.Fatalf("%s: line %d", key, d.Spans[key].Line)
				}
			}
			// Every accepted grammar fixture also checks a scalar edit and no-op.
			updates := map[string]any{"max_rounds": int64(9)}
			out, changed, err := d.Replace(updates)
			if err != nil || !changed {
				t.Fatalf("Edit: %v %v", changed, err)
			}
			if span, exists := d.Spans["max_rounds"]; exists {
				want := append(append(append([]byte{}, d.Bytes[:span.Start]...), []byte("9")...), d.Bytes[span.End:]...)
				if !bytes.Equal(out, want) {
					t.Fatalf("Unrelated bytes changed: %q", out)
				}
			} else {
				want := f.text
				if len(want) > 0 && want[len(want)-1] != '\n' {
					want += "\n"
				}
				want += "max_rounds = 9\n"
				if string(out) != want {
					t.Fatalf("Append changed bytes: %q", out)
				}
			}
			same, changed, err := d.Replace(d.Values)
			if err != nil || changed || !bytes.Equal(same, d.Bytes) {
				t.Fatalf("No-op: %v %v", changed, err)
			}
		})
	}
	rejected := []struct {
		name, text, key string
		line            int
	}{
		{"duplicate", "# heading\nplanner='claude'\nplanner='codex'\n", "planner", 3},
		{"table", "# heading\n[profile]\nplanner='claude'\n", "profile", 2},
		{"array table", "[[profiles]]\nplanner='claude'", "profiles", 1},
		{"dotted", "# heading\nplanner.value='claude'", "planner", 2},
		{"quoted", "# heading\n\"planner\"='claude'", "planner", 2},
		{"literal quoted", "'planner'='claude'", "planner", 1},
		{"unknown", "# header\nunknown=8", "unknown", 2},
		{"wrong boolean", "# header\npersistent='true'", "persistent", 2},
		{"wrong integer", "max_rounds=8.0", "max_rounds", 1},
		{"integer bound", "max_rounds=0", "max_rounds", 1},
		{"wrong array element", "claude_critic_tools=[1]", "claude_critic_tools", 1},
		{"forbidden tool", "claude_critic_tools=['Edit']", "claude_critic_tools", 1},
		{"duplicate tool", "claude_critic_tools=['Read','Read']", "claude_critic_tools", 1},
		{"empty tool", "claude_critic_tools=[]", "claude_critic_tools", 1},
		{"inline table", "persistent={a=true}", "persistent", 1},
		{"date", "claude_model=2026-10-04", "claude_model", 1},
		{"bad duration", "poll_interval='9ms'", "poll_interval", 1},
		{"control identifier", "claude_model=\"a\\nb\"", "claude_model", 1},
		{"option identifier", "codex_effort='--x'", "codex_effort", 1},
		{"NUL path", "context_dir=\"a\\u0000b\"", "context_dir", 1},
	}
	for _, f := range rejected {
		t.Run(f.name, func(t *testing.T) {
			_, err := Parse("settings.toml", []byte(f.text))
			var bad *Invalid
			if !errors.As(err, &bad) {
				t.Fatalf("Missing diagnostic: %v", err)
			}
			if bad.Key != f.key || bad.Line != f.line || bad.File != "settings.toml" {
				t.Fatalf("Diagnostic: %#v", bad)
			}
		})
	}
	for _, b := range [][]byte{[]byte("planner = 'claude"), []byte("planner = ["), {0xff}, bytes.Repeat([]byte{' '}, input.Limit+1)} {
		if _, err := Parse("settings.toml", b); err == nil {
			t.Fatal("Malformed input accepted")
		}
	}
	if _, err := Parse("settings.toml", bytes.Repeat([]byte{' '}, input.Limit)); err != nil {
		t.Fatal(err)
	}
}

func TestACFG02ValueRangesAndFailedEdits(t *testing.T) {
	text := "# original\r\ncontext_dir = '''\r\nA\\B\r\n日本''' # keep\r\nclaude_planner_tools = [\r\n 'Read', # inner\r\n 'Skill',\r\n] # after\r\npersistent\t= false  # last\r\n"
	d, err := Parse("settings.toml", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		key      string
		value    any
		rendered string
	}{
		{"context_dir", "new", "'new'"}, {"claude_planner_tools", []string{"Read", "Write"}, "['Read', 'Write']"}, {"persistent", true, "true"},
	} {
		t.Run(test.key, func(t *testing.T) {
			b, changed, err := d.Replace(map[string]any{test.key: test.value})
			if err != nil || !changed {
				t.Fatal(err)
			}
			span := d.Spans[test.key]
			want := append(append(append([]byte{}, d.Bytes[:span.Start]...), []byte(test.rendered)...), d.Bytes[span.End:]...)
			if !bytes.Equal(b, want) {
				t.Fatalf("Value range changed unrelated bytes:\n%q\nwant %q", b, want)
			}
		})
	}
	for _, updates := range []map[string]any{{"planner": "bad"}, {"unknown": true}, {"max_rounds": 0}, {"persistent": "true"}} {
		out, changed, err := d.Replace(updates)
		if err == nil || out != nil || changed {
			t.Fatalf("Failed edit returned a candidate: %q %v %v", out, changed, err)
		}
		if string(d.Bytes) != text {
			t.Fatal("Failed edit changed the original")
		}
	}
	b, _, err := d.Replace(map[string]any{"backend": "gashki", "max_rounds": 7, "planner": "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("planner = 'codex'\r\nbackend = 'gashki'\r\nmax_rounds = 7\r\n")) {
		t.Fatalf("Append order or CRLF: %q", b)
	}
	noNewline, _ := Parse("settings.toml", []byte("# end"))
	b, _, err = noNewline.Replace(map[string]any{"persistent": true})
	if err != nil || string(b) != "# end\npersistent = true\n" {
		t.Fatalf("Final newline: %q %v", b, err)
	}
}
func TestACFG01RegularFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "settings.toml")
	if err := os.WriteFile(file, []byte("planner='codex'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(file); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root); err == nil {
		t.Fatal("Directory accepted")
	}
	if err := os.Symlink(file, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
}
