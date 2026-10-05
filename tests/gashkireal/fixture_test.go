// Tier B runs only with an explicit, hash-checked fixture. Ordinary Go tests
// skip these mechanics cases. A skip never supplies acceptance evidence.
package gashkireal

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/sys/unix"
)

const pin = "8eaecc9b31c965bab6c63a6a5562ebf38c43e363"

var fixtureTestHash = sync.OnceValues(func() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
})

type fixture struct {
	t                                                   *testing.T
	dir, root, socket, socketPath, caller, config, tmux string
	env                                                 []string
	compiler                                            *jsonschema.Compiler
	schemas                                             map[string]*jsonschema.Schema
	socketInfo                                          os.FileInfo
	calls, spawns                                       int
}

func option(name string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}
func read(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func object(t *testing.T, b []byte) map[string]any {
	t.Helper()
	v, e := input.Record(bytes.NewReader(b), 32<<20)
	if e != nil {
		t.Fatalf("strict JSON: %v: %.1000s", e, b)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatal("object required")
	}
	return m
}
func member(m map[string]any, k string) map[string]any { x, _ := m[k].(map[string]any); return x }
func text(m map[string]any, k string) string           { s, _ := m[k].(string); return s }
func put(t *testing.T, p string, b []byte) {
	t.Helper()
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func number(v any) int { n, _ := v.(json.Number); i, _ := n.Int64(); return int(i) }
func jsonBytes(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}

type offline struct{}

func (offline) Load(url string) (any, error) { return nil, fmt.Errorf("unregistered schema %s", url) }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := option("--fixture")
	if dir == "" {
		t.Skip("tier B requires --fixture; no acceptance pass")
	}
	dir, e := filepath.Abs(dir)
	if e != nil {
		t.Fatal(e)
	}
	var space unix.Statfs_t
	if e := unix.Statfs(dir, &space); e != nil || uint64(space.Bavail)*uint64(space.Bsize) < 2<<30 {
		t.Fatal("fixture requires 2 GiB free space", e)
	}
	m := object(t, read(t, filepath.Join(dir, "fixture.json")))
	if text(m, "source_commit") != pin || !strings.HasPrefix(text(m, "target"), runtime.GOOS+"/") {
		t.Fatal("wrong source or target")
	}
	for name, raw := range member(m, "binaries") {
		record := raw.(map[string]any)
		path := filepath.Join(dir, name)
		if contract.HashBytes(read(t, path)) != text(record, "sha256") {
			t.Fatal("binary hash", name)
		}
		info, e := buildinfo.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		tags := ""
		for _, s := range info.Settings {
			if s.Key == "-tags" {
				tags = s.Value
			}
		}
		if name == "gashki-release" && tags != "" || name == "gashki-fault" && tags != "gashkitest" {
			t.Fatal("binary build tags", name, tags)
		}
	}
	for name, hash := range member(m, "files") {
		if contract.HashBytes(read(t, filepath.Join(dir, name))) != hash {
			t.Fatal("fixture file hash", name)
		}
	}
	root, e := os.MkdirTemp(dir, "proof-")
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{t: t, dir: dir, root: root, schemas: make(map[string]*jsonschema.Schema)}
	for _, name := range []string{"home", "config", "state", "data", "cache", "runtime", "tmp", "tools", "stub", "ws", "hold"} {
		if e := os.Mkdir(filepath.Join(root, name), 0700); e != nil {
			t.Fatal(e)
		}
	}
	for _, name := range []string{"sh", "stty", "cat", "ps", "perl", "sleep"} {
		path := "/bin/" + name
		if name == "perl" {
			path = "/usr/bin/" + name
		}
		if _, e := os.Stat(path); e != nil {
			t.Fatal("required utility", path, e)
		}
		if e := os.Symlink(path, filepath.Join(root, "tools", name)); e != nil {
			t.Fatal(e)
		}
	}
	f.tmux = "/usr/bin/tmux"
	if runtime.GOOS == "darwin" {
		f.tmux = "/opt/homebrew/bin/tmux"
	}
	if e := os.Symlink(f.tmux, filepath.Join(root, "tools", "tmux")); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"claude", "codex"} {
		p := filepath.Join(root, "tools", name)
		put(t, p, read(t, filepath.Join(dir, "agent-stub")))
		if e := os.Chmod(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	short, e := os.MkdirTemp("/tmp", "vgk-")
	if e != nil {
		t.Fatal(e)
	}
	short, e = filepath.EvalSymlinks(short)
	if e != nil {
		t.Fatal(e)
	}
	var random [8]byte
	if _, e := rand.Read(random[:]); e != nil {
		t.Fatal(e)
	}
	f.socket = "volley-" + hex.EncodeToString(random[:])
	put(t, filepath.Join(root, "ownership.json"), jsonBytes(map[string]any{"tmux_temp": short, "server_name": f.socket}))
	f.env = []string{"HOME=" + filepath.Join(root, "home"), "PATH=" + filepath.Join(root, "tools"), "TMPDIR=" + filepath.Join(root, "tmp"), "TMUX_TMPDIR=" + short, "SHELL=/bin/sh", "TERM=xterm-256color", "LC_ALL=C", "STUB_DIR=" + filepath.Join(root, "stub")}
	for key, name := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "runtime"} {
		f.env = append(f.env, key+"="+filepath.Join(root, name))
	}
	f.config = filepath.Join(root, "gashki.toml")
	put(t, f.config, []byte(fmt.Sprintf("state_dir = %q\ntmux_socket = %q\nready_timeout = \"15s\"\nwait_timeout = \"15s\"\n", filepath.Join(root, "state", "gashki"), f.socket)))
	f.compiler = jsonschema.NewCompiler()
	f.compiler.UseLoader(offline{})
	data := object(t, read(t, filepath.Join(dir, "schemas.json")))
	add := func(v any) {
		m := v.(map[string]any)
		if e := f.compiler.AddResource(text(m, "$id"), v); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range member(data, "definitions") {
		add(v)
	}
	add(data["envelope_schema"])
	for _, v := range member(data, "schemas") {
		add(v)
	}
	for name, v := range member(data, "schemas") {
		s, e := f.compiler.Compile(text(v.(map[string]any), "$id"))
		if e != nil {
			t.Fatal(e)
		}
		f.schemas[name] = s
	}
	s, e := f.compiler.Compile(text(member(data, "envelope_schema"), "$id"))
	if e != nil {
		t.Fatal(e)
	}
	f.schemas["envelope"] = s
	// Contract checks run before a tmux server or provider process is started.
	f.preflight()
	f.tm("new-session", "-d", "-s", "caller", "-x", "320", "-y", "60", "/bin/cat")
	f.socketPath = f.tm("display-message", "-p", "#{socket_path}")
	f.caller = f.tm("display-message", "-p", "#{pane_id}")
	f.socketInfo, e = os.Stat(f.socketPath)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		info, e := os.Stat(f.socketPath)
		if e == nil && !os.SameFile(info, f.socketInfo) {
			t.Error("owned socket changed; cleanup refused")
			return
		}
		owned := map[int]string{}
		if e == nil {
			serverPID, err := strconv.Atoi(f.tm("display-message", "-p", "#{pid}"))
			if err != nil {
				t.Error(err)
				return
			}
			all := f.processes()
			if p, ok := all[serverPID]; ok {
				owned[serverPID] = p.start
			} else {
				t.Error("server process missing before cleanup")
				return
			}
			for changed := true; changed; {
				changed = false
				for pid, p := range all {
					if _, ok := owned[p.parent]; ok {
						if _, seen := owned[pid]; !seen {
							owned[pid] = p.start
							changed = true
						}
					}
				}
			}
			put(t, filepath.Join(root, "cleanup-processes.json"), jsonBytes(owned))
			f.tm("kill-server")
		}
		deadline := time.Now().Add(5 * time.Second)
		settled := false
		for time.Now().Before(deadline) {
			all := f.processes()
			live := false
			for pid, start := range owned {
				if p, ok := all[pid]; ok && p.start == start && !p.zombie {
					live = true
				}
			}
			if !live {
				settled = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !settled {
			t.Error("owned server did not stop")
			return
		}
		if e := os.RemoveAll(short); e != nil {
			t.Error(e)
		}
		put(t, filepath.Join(root, "summary.json"), jsonBytes(map[string]any{"fixture": "F-GK-REAL", "os": runtime.GOOS, "source_commit": pin, "socket": f.socketPath, "calls": f.calls, "provider_spawns": f.spawns, "owned_server_stopped": true}))
		// Keep compact evidence, then remove this exact owned fixture. Failure to
		// establish process settlement above deliberately retains the root.
		hashes := map[string]string{}
		var size int64
		e = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += info.Size()
			if size > 512<<20 {
				return fmt.Errorf("fixture exceeds 512 MiB limit")
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, file)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			hashes[rel] = hex.EncodeToString(h.Sum(nil))
			return nil
		})
		if e != nil {
			t.Error(e)
			return
		}
		counts := map[string]any{}
		for _, provider := range []string{"claude", "codex"} {
			load := func(suffix string) string {
				b, err := os.ReadFile(filepath.Join(root, "stub", provider+suffix))
				if os.IsNotExist(err) {
					return ""
				}
				if err != nil {
					t.Error(err)
					return ""
				}
				return string(b)
			}
			counts[provider] = map[string]any{"launches": strings.Count(load(".launches"), "1\n"), "pastes": strings.Count(load(".keys"), "<Paste>\n"), "prompt_hooks": strings.Count(load(".hooks"), "UserPromptSubmit\n"), "stop_hooks": strings.Count(load(".hooks"), "Stop\n")}
		}
		testHash, err := fixtureTestHash()
		if err != nil {
			t.Error(err)
			return
		}
		put(t, root+"-evidence.json", jsonBytes(map[string]any{"test": t.Name(), "passed": !t.Failed(), "source_commit": pin, "target": text(m, "target"), "binaries": m["binaries"], "controller_test_binary_sha256": testHash, "owned_provider_counts": counts, "bytes": size, "artifacts": hashes, "owned_server_stopped": true}))
		if option("--retain-fixtures") != "yes" {
			if e := os.RemoveAll(root); e != nil {
				t.Error(e)
			}
		}
	})
	if !strings.HasPrefix(f.socketPath, short+string(os.PathSeparator)) || f.socketInfo.Mode()&os.ModeSocket == 0 {
		t.Fatal("foreign server socket", f.socketPath, short)
	}
	t.Log("owned proof", root)
	return f
}

func (f *fixture) tm(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, f.tmux, append([]string{"-f", "/dev/null", "-L", f.socket}, args...)...)
	c.Env = f.env
	b, e := c.CombinedOutput()
	if e != nil {
		f.t.Fatalf("owned tmux %q: %v: %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}

type processFact struct {
	parent int
	start  string
	zombie bool
}

func (f *fixture) processes() map[int]processFact {
	f.t.Helper()
	c := exec.Command("/bin/ps", "-axo", "pid=,ppid=,lstart=,state=")
	c.Env = f.env
	b, e := c.Output()
	if e != nil {
		f.t.Fatal(e)
	}
	all := map[int]processFact{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 8 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		if e1 == nil && e2 == nil {
			all[pid] = processFact{parent, strings.Join(fields[2:7], " "), strings.HasPrefix(fields[7], "Z")}
		}
	}
	return all
}

func (f *fixture) call(bin, verb, stdin string, extra []string, args ...string) (int, map[string]any) {
	f.t.Helper()
	f.calls++
	argv := append([]string{"--config=" + f.config, "--json"}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, filepath.Join(f.dir, bin), argv...)
	c.Env = append(append([]string{}, f.env...), extra...)
	c.Dir = filepath.Join(f.root, "ws")
	if stdin != "" {
		c.Stdin = strings.NewReader(stdin)
	}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	code := 0
	if e != nil {
		if ee, ok := e.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			f.t.Fatal(e)
		}
	}
	prefix := filepath.Join(f.root, fmt.Sprintf("call-%03d", f.calls))
	put(f.t, prefix+".stdout", b)
	put(f.t, prefix+".stderr", stderr.Bytes())
	put(f.t, prefix+".request.json", jsonBytes(map[string]any{"binary": bin, "verb": verb, "args": argv, "stdin_hash": contract.HashBytes([]byte(stdin)), "exit": code}))
	m := object(f.t, b)
	if e := f.schemas["envelope"].Validate(m); e != nil {
		f.t.Fatalf("%s envelope: %v: %s", verb, e, b)
	}
	meta := member(m, "meta")
	if text(meta, "contract_version") != "2" || text(meta, "data_hash") != "sha256:"+contract.HashBytes(canonical(m["data"])) {
		f.t.Fatalf("%s unchecked version/hash", verb)
	}
	if code == 0 {
		if m["ok"] != true || len(m["errors"].([]any)) != 0 {
			f.t.Fatal("success status mismatch")
		}
		if verb == "version" {
			if text(m, "tool_version") == "" || text(member(m, "data"), "contract_version") != "2" || len(member(m, "data")) != 2 || len(member(member(m, "data"), "build")) != 2 {
				f.t.Fatal("version facts", m)
			}
			return code, m
		}
		if f.schemas[verb] == nil {
			f.t.Fatal("missing data schema", verb)
		}
		if e := f.schemas[verb].Validate(m["data"]); e != nil {
			f.t.Fatalf("%s data schema: %v: %s", verb, e, b)
		}
	} else {
		if m["ok"] != false || len(m["errors"].([]any)) == 0 || number(memberArray(m, "errors", 0)["exit_code"]) != code {
			f.t.Fatal("failure status mismatch")
		}
	}
	return code, m
}
func memberArray(m map[string]any, k string, i int) map[string]any {
	a, _ := m[k].([]any)
	if i >= len(a) {
		return nil
	}
	return a[i].(map[string]any)
}
func (f *fixture) ok(verb, stdin string, args ...string) map[string]any {
	f.t.Helper()
	c, m := f.call("gashki-release", verb, stdin, nil, args...)
	if c != 0 {
		f.t.Fatalf("%s: %s", verb, jsonBytes(m))
	}
	return member(m, "data")
}
func (f *fixture) preflight() {
	f.ok("version", "", "--version")
	caps := f.ok("capabilities", "", "capabilities")
	want := object(f.t, read(f.t, filepath.Join(f.dir, "capabilities.json")))
	if !reflect.DeepEqual(caps, want) {
		f.t.Fatal("pinned capabilities mismatch")
	}
	got := f.ok("schema", "", "schema")
	want = object(f.t, read(f.t, filepath.Join(f.dir, "schemas.json")))
	if !reflect.DeepEqual(got, want) {
		f.t.Fatal("pinned public schemas mismatch")
	}
	verbs := member(caps, "verbs")
	requirements := map[string]map[string]int{"spawn": {"--agent": 1, "--cwd": 1, "--agent-args": 1, "--here": 0, "--trust-folder": 0}, "send": {"--from-stdin": 0, "--idempotency-key": 1}, "wait": {"--until": 1, "--since": 1, "--wait-timeout": 1}, "kill": {"--yes": 0}, "observe": {}, "status": {}}
	for verb, flags := range requirements {
		entry := member(verbs, verb)
		if entry == nil || entry["json"] != true || f.schemas[verb] == nil {
			f.t.Fatal("missing contract verb", verb)
		}
		for name, arity := range flags {
			found := false
			for _, raw := range entry["flags"].([]any) {
				flag := raw.(map[string]any)
				if text(flag, "name") == name && number(flag["arity"]) == arity {
					found = true
				}
			}
			if !found {
				f.t.Fatal("missing flag", verb, name)
			}
		}
	}
	for _, verb := range []string{"show", "get"} {
		if member(member(member(verbs, "config"), "subcommands"), verb) == nil || f.schemas["config "+verb] == nil {
			f.t.Fatal("missing config contract", verb)
		}
	}
	if d := f.ok("config get", "", "config", "get", "state_dir"); d["value"] != filepath.Join(f.root, "state", "gashki") {
		f.t.Fatal("wrong state root")
	}
	f.ok("config show", "", "config", "show")
}

func (f *fixture) proveProvider(name string) {
	f.t.Helper()
	path := filepath.Join(f.root, "tools", name)
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || contract.HashBytes(read(f.t, path)) != contract.HashBytes(read(f.t, filepath.Join(f.dir, "agent-stub"))) {
		f.t.Fatal("unowned provider path", path)
	}
	for _, entry := range f.env {
		if strings.HasPrefix(entry, "PATH=") && entry != "PATH="+filepath.Join(f.root, "tools") {
			f.t.Fatal("provider fallback PATH")
		}
	}
}

func (f *fixture) spawn(provider, role, placement, mode string) map[string]any {
	f.t.Helper()
	f.proveProvider(provider)
	stub := filepath.Join(f.root, "stub")
	put(f.t, filepath.Join(stub, provider+".mode"), []byte(mode))
	// Fixed arguments check merge behavior independently of Volley's adapter.
	args := []string{"--model", "fixture-model", "--effort", "high"}
	if provider == "claude" {
		tools := "Read,Glob,Grep,Skill"
		allow := []string{"Read(//" + strings.TrimPrefix(filepath.Join(f.root, "ws"), "/") + "/**)"}
		if role == "planner" {
			tools += ",Edit,Write"
			allow = append(allow, "Edit(//"+strings.TrimPrefix(filepath.Join(f.root, "ws"), "/")+"/**)")
		}
		settings := map[string]any{"permissions": map[string]any{"allow": allow, "deny": []string{"Edit(//" + strings.TrimPrefix(filepath.Join(f.root, "state"), "/") + "/**)"}}, "hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": ":"}}}}}}
		args = append(args, "--permission-mode", "dontAsk", "--tools", tools, "--settings", string(jsonBytes(settings)))
	} else {
		sandbox := "read-only"
		if role == "planner" {
			sandbox = "workspace-write"
		}
		args = []string{"--model", "fixture-model", "--sandbox", sandbox, "-c", `model_reasoning_effort="high"`}
	}
	argv := []string{"spawn", "proof/" + role, "--agent=" + provider, "--cwd=" + filepath.Join(f.root, "ws"), "--agent-args=" + string(jsonBytes(args))}
	if placement == "here" {
		argv = append(argv, "--here")
		f.env = append(f.env, "TMUX="+f.socketPath+",1,0", "TMUX_PANE="+f.caller)
	}
	if mode == "trust" {
		argv = append(argv, "--trust-folder")
	}
	d := f.ok("spawn", "", argv...)
	f.spawns++
	if d["existing"] != false || text(d, "agent") != provider || text(d, "id") == "" {
		f.t.Fatal("spawn fields")
	}
	if f.tm("display-message", "-p", "-t", text(d, "target"), "#{@gashki_id}") != text(d, "id") {
		f.t.Fatal("pane UUID mismatch")
	}
	if placement == "here" && f.tm("display-message", "-p", "-t", text(d, "target"), "#{window_id}") != f.tm("display-message", "-p", "-t", f.caller, "#{window_id}") {
		f.t.Fatal("here placement")
	}
	obs := f.ok("observe", "", "observe", text(d, "id"))
	cursor := text(obs, "cursor")
	if cursor == "" || obs["safe_to_send"] != true {
		f.t.Fatal("ready cursor/sendability", obs)
	}
	ready := f.ok("wait", "", "wait", text(d, "id"), "--until=idle", "--wait-timeout=2s")
	ev := member(ready, "event")
	if text(ev, "kind") != "pane_ready" || text(ready, "id") != text(d, "id") || text(member(ev, "detail"), "name") != text(d, "name") || text(member(ev, "detail"), "socket") != f.socketPath {
		f.t.Fatal("ready socket evidence", ready)
	}
	if text(ready, "cursor") != cursor {
		f.t.Fatal("ready cursor changed without a turn")
	}
	f.checkArgs(provider, role, args)
	environment := string(read(f.t, filepath.Join(f.root, "stub", provider+".env")))
	for _, assignment := range []string{"HOME=" + filepath.Join(f.root, "home"), "PATH=" + filepath.Join(f.root, "tools"), "GASHKI_STATE_DIR=" + filepath.Join(f.root, "state", "gashki"), "XDG_CONFIG_HOME=" + filepath.Join(f.root, "config")} {
		if !contains(strings.Split(environment, "\n"), assignment) {
			f.t.Fatal("effective root changed", assignment)
		}
	}
	return d
}

func (f *fixture) checkArgs(provider, role string, want []string) {
	f.t.Helper()
	got := strings.Split(strings.TrimSuffix(string(read(f.t, filepath.Join(f.root, "stub", provider+".argv"))), "\n"), "\n")
	if provider == "claude" {
		count := 0
		var settings map[string]any
		for i, a := range got {
			if a == "--settings" {
				count++
				settings = object(f.t, []byte(got[i+1]))
			}
		}
		if count != 1 || len(member(settings, "hooks")) != 10 || len(member(settings, "permissions")) != 2 {
			f.t.Fatal("merged hooks/rules", got)
		}
		for event, v := range member(settings, "hooks") {
			entries := v.([]any)
			hook := memberArray(entries[0].(map[string]any), "hooks", 0)
			if event == "PreToolUse" {
				if text(hook, "command") != ":" {
					f.t.Fatal("caller hook lost")
				}
				continue
			}
			if !strings.Contains(text(hook, "command"), filepath.Join(f.dir, "gashki-release")) {
				f.t.Fatal("wrong hook executable")
			}
		}
		for _, a := range []string{"--permission-mode", "dontAsk", "--tools", "fixture-model", "high"} {
			if !contains(got, a) {
				f.t.Fatal("lost argument", a)
			}
		}
		tools := "Read,Glob,Grep,Skill"
		if role == "planner" {
			tools += ",Edit,Write"
		}
		if !contains(got, tools) {
			f.t.Fatal("role tools")
		}
		for i, a := range want {
			if a == "--settings" {
				original := object(f.t, []byte(want[i+1]))
				if !reflect.DeepEqual(settings["permissions"], original["permissions"]) {
					f.t.Fatal("role rule order or contents changed")
				}
			}
		}
	} else {
		hooks := 0
		for _, a := range got {
			if strings.HasPrefix(a, "hooks.") {
				hooks++
				if !strings.Contains(a, "hook-event") {
					f.t.Fatal("hook command")
				}
			}
		}
		if hooks != 6 {
			f.t.Fatal("codex hook count", hooks)
		}
		for _, a := range want {
			if !contains(got, a) {
				f.t.Fatal("lost codex argument", a)
			}
		}
	}
	put(f.t, filepath.Join(f.root, provider+"-"+role+"-argv.json"), jsonBytes(got))
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

func TestPinnedContractAndReleaseTriggers(t *testing.T) {
	f := newFixture(t)
	code, m := f.call("gashki-fault", "capabilities", "", []string{"GASHKI_TEST_FAULT=1"}, "capabilities")
	if code != 6 || text(memberArray(m, "errors", 0), "code") != "INTERNAL" {
		t.Fatal("fault build seam inactive")
	}
	code, _ = f.call("gashki-release", "capabilities", "", []string{"GASHKI_TEST_FAULT=1"}, "capabilities")
	if code != 0 {
		t.Fatal("release fault activated")
	}
	d := f.spawn("claude", "planner", "normal", "idle")
	extra := []string{"GASHKI_TEST_FAULT=send-barrier", "GASHKI_TEST_HOLD=send-barrier:" + filepath.Join(f.root, "hold")}
	start := time.Now()
	code, m = f.call("gashki-release", "send", "release seam test", extra, "send", text(d, "id"), "--from-stdin", "--idempotency-key=release-inert")
	if code != 0 || time.Since(start) >= 8*time.Second || member(m, "data")["submitted"] != true {
		t.Fatal("release named seam activated", m)
	}
	if entries, e := os.ReadDir(filepath.Join(f.root, "hold")); e != nil || len(entries) != 0 {
		t.Fatal("release hold wrote a trigger", entries, e)
	}
	s := member(m, "data")
	done := f.ok("wait", "", "wait", text(d, "id"), "--until=idle", "--since="+text(s, "turn_cursor"), "--wait-timeout=5s")
	if text(member(done, "event"), "kind") != "turn_ended" || text(member(done, "event"), "source") != "hook" {
		t.Fatal("hook completion", done)
	}
	f.assertCounts("claude", 1, 1)
	f.ok("kill", "", "kill", text(d, "id"), "--yes")
	t.Log("A-GK-01 tier B: pinned builds, checked contract, owned roots, inert release seams, one process and paste")
}

func (f *fixture) assertCounts(provider string, processes, pastes int) {
	f.t.Helper()
	dir := filepath.Join(f.root, "stub")
	argv := string(read(f.t, filepath.Join(dir, provider+".argv")))
	keys := string(read(f.t, filepath.Join(dir, provider+".keys")))
	hooks := string(read(f.t, filepath.Join(dir, provider+".hooks")))
	if strings.Count(argv, "--model\n") != processes || strings.Count(keys, "<Paste>\n") != pastes {
		f.t.Fatal("independent process/paste count", argv, keys)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		if strings.Count(hooks, event+"\n") != 1 {
			f.t.Fatal("independent hook count", event, hooks)
		}
	}
	put(f.t, filepath.Join(f.root, provider+"-counts.json"), jsonBytes(map[string]any{"processes": processes, "pastes": pastes, "hook_count": 3, "argv_sha256": contract.HashBytes([]byte(argv)), "keys_sha256": contract.HashBytes([]byte(keys)), "hooks_sha256": contract.HashBytes([]byte(hooks))}))
}

func TestSpawnMechanisms(t *testing.T) {
	for _, assignment := range []string{"claude-planner", "codex-planner"} {
		for _, placement := range []string{"normal", "here"} {
			for _, mode := range []string{"idle", "trust"} {
				t.Run(assignment+"/"+placement+"/"+mode, func(t *testing.T) {
					f := newFixture(t)
					before := f.tm("display-message", "-p", "-t", f.caller, "#{pane_id} #{pane_pid} #{pane_current_command} #{window_id} #{session_name}")
					planner, critic := "claude", "codex"
					if assignment == "codex-planner" {
						planner, critic = critic, planner
					}
					panes := []map[string]any{f.spawn(planner, "planner", placement, mode), f.spawn(critic, "critic", placement, mode)}
					if text(panes[0], "id") == text(panes[1], "id") || text(panes[0], "target") == text(panes[1], "target") {
						t.Fatal("pane identity collision")
					}
					for i, d := range panes {
						// An exact repeat retains the existing pane; a different provider conflicts.
						f.proveProvider(text(d, "agent"))
						same := f.ok("spawn", "", "spawn", text(d, "name"), "--agent="+text(d, "agent"), "--cwd="+filepath.Join(f.root, "ws"))
						if same["existing"] != true || same["id"] != d["id"] {
							t.Fatal("retained pane")
						}
						other := "claude"
						if text(d, "agent") == other {
							other = "codex"
						}
						f.proveProvider(other)
						code, m := f.call("gashki-release", "spawn", "", nil, "spawn", text(d, "name"), "--agent="+other)
						if code != 5 {
							t.Fatal("provider conflict", m)
						}
						selector := text(d, "id")
						if i == 1 {
							selector = text(d, "target")
						}
						send := f.ok("send", fmt.Sprintf("fixture turn %d", i), "send", selector, "--from-stdin", fmt.Sprintf("--idempotency-key=turn-%d", i))
						if send["submitted"] != true || send["replayed"] != false || text(send, "barrier_cursor") == "" || text(send, "turn_cursor") == "" {
							t.Fatal("send fields", send)
						}
						done := f.ok("wait", "", "wait", text(d, "name"), "--until=idle", "--since="+text(send, "turn_cursor"), "--wait-timeout=5s")
						if text(member(done, "event"), "kind") != "turn_ended" || text(member(done, "event"), "source") != "hook" {
							t.Fatal("independent hook result", done)
						}
						f.assertCounts(text(d, "agent"), 1, 1)
					}
					status := f.ok("status", "", "status")
					if len(status["items"].([]any)) != 2 {
						t.Fatal("status selection", status)
					}
					for _, d := range panes {
						f.ok("kill", "", "kill", text(d, "id"), "--yes")
					}
					if after := f.tm("display-message", "-p", "-t", f.caller, "#{pane_id} #{pane_pid} #{pane_current_command} #{window_id} #{session_name}"); after != before {
						t.Fatal("caller changed", before, after)
					}
					if ids := f.tm("list-panes", "-a", "-F", "#{pane_id}"); ids != f.caller {
						t.Fatal("fixture pane remains", ids)
					}
					t.Log("A-GK-02 tier B: fixed settings merge, role tools/model/effort, ready UUID/socket/cursor, conflicts, selectors, retained panes, independent process/paste counts")
				})
			}
		}
	}
}

func TestIsolationAndTrustRefusals(t *testing.T) {
	f := newFixture(t)
	for _, mode := range []string{"trust", "trust-wrong"} {
		f.proveProvider("claude")
		put(t, filepath.Join(f.root, "stub", "claude.mode"), []byte(mode))
		args := []string{"spawn", "refusal/" + mode, "--agent=claude", "--cwd=" + filepath.Join(f.root, "ws"), "--ready-timeout=1s"}
		if mode == "trust-wrong" {
			args = append(args, "--trust-folder")
		}
		code, m := f.call("gashki-release", "spawn", "", nil, args...)
		if code != 3 || text(memberArray(m, "errors", 0), "code") != "LAUNCH_PROMPT_UNHANDLED" {
			t.Fatal("unapproved or mismatched folder trust accepted", m)
		}
		if f.tm("list-panes", "-a", "-F", "#{pane_id}") != f.caller {
			t.Fatal("refused startup pane retained")
		}
	}
	if e := os.Remove(filepath.Join(f.root, "tools", "codex")); e != nil {
		t.Fatal(e)
	}
	code, m := f.call("gashki-release", "spawn", "", nil, "spawn", "refusal/missing", "--agent=codex")
	if code != 3 || text(memberArray(m, "errors", 0), "code") != "AGENT_CLI_MISSING" {
		t.Fatal("missing owned provider fell back", m)
	}
	if _, e := os.Stat(filepath.Join(f.root, "stub", "codex.argv")); !os.IsNotExist(e) {
		t.Fatal("missing provider launched")
	}
	t.Log("A-GK-01/02 negative variants: absent provider cannot fall back; unapproved and wrong-path folder screens are refused")
}
