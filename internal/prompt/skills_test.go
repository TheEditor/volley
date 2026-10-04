package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeSkill(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Required skill\nApply its standard.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func containsPath(paths []string, path string) bool {
	for _, p := range paths {
		if p == path {
			return true
		}
	}
	return false
}

func TestSkillsLinkedRoots(t *testing.T) {
	owned := t.TempDir()
	root := filepath.Join(owned, "config")
	skills := filepath.Join(root, "skills")
	if err := os.MkdirAll(skills, 0700); err != nil {
		t.Fatal(err)
	}
	target := makeSkill(t, filepath.Join(owned, "linked [safe]*?"), "standard")
	if err := os.Symlink(target, filepath.Join(skills, "standard")); err != nil {
		t.Fatal(err)
	}
	nested := makeSkill(t, filepath.Join(owned, "nested"), "extra")
	if err := os.Symlink(nested, filepath.Join(target, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(nested, "cycle")); err != nil {
		t.Fatal(err)
	}
	context := filepath.Join(owned, "context")
	if err := os.Mkdir(context, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("private_marker = 'do not copy'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run("A-PROMPT-02/linked/"+provider, func(t *testing.T) {
			p, err := PlanSkills(SkillOptions{Provider: provider, EffectiveRoot: root, ContextPath: context, Required: []string{"standard"}})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{skills, target, nested, context} {
				physical, err := filepath.EvalSymlinks(path)
				if err != nil {
					t.Fatal(err)
				}
				if !containsPath(p.ReadRoots, path) && !containsPath(p.ReadRoots, physical) {
					t.Fatal("Unrecorded root", path)
				}
			}
			for _, rule := range p.ReadRules {
				if !strings.HasPrefix(rule, "Read(//") || strings.Contains(rule, "Edit(") || strings.Contains(rule, "Write(") {
					t.Fatal("Write permission or non-absolute rule", rule)
				}
			}
			if provider == "claude" && (p.SystemInstruction == "" || strings.Count(p.SystemInstruction, p.SkillsPath) != 1 || len(p.ReadRules) != len(p.ReadRoots)) {
				t.Fatal("Claude skill instruction")
			}
			if provider == "codex" && (p.SystemInstruction != "" || len(p.ReadRules) != 0) {
				t.Fatal("Codex environment must remain its identity source")
			}
			scratch := t.TempDir()
			if err := os.Chmod(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			sp, err := LinkScratchSkills(scratch, p)
			if err != nil {
				t.Fatal(err)
			}
			if !containsPath(sp.ReadRoots, target) {
				physical, _ := filepath.EvalSymlinks(target)
				if !containsPath(sp.ReadRoots, physical) {
					t.Fatal("Scratch link target missing")
				}
			}
			if _, err := os.Lstat(filepath.Join(scratch, "config.toml")); !os.IsNotExist(err) {
				t.Fatal("Private config copied")
			}
			link, err := os.Lstat(filepath.Join(scratch, "skills", "standard"))
			if err != nil || link.Mode()&os.ModeSymlink == 0 {
				t.Fatal("Required scratch skill link missing", err)
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 1 || entries[0].Name() != "skills" {
				t.Fatal("Unexpected scratch contents")
			}
		})
	}
}

func TestMissingSkillsBeforeClaim(t *testing.T) {
	root := t.TempDir()
	for _, provider := range []string{"claude", "codex"} {
		for _, effective := range []string{root, ""} {
			claims := 0
			p, err := PlanSkills(SkillOptions{Provider: provider, EffectiveRoot: effective, Required: []string{"absent"}})
			if err == nil {
				claims++
			}
			if claims != 0 || len(p.Unavailable) == 0 {
				t.Fatal("Missing standard was claimed")
			}
		}
	}
	optional, err := PlanSkills(SkillOptions{Provider: "claude", EffectiveRoot: root})
	if err != nil || len(optional.Unavailable) == 0 {
		t.Fatal("Missing optional directory not reported", err)
	}
	scratch := t.TempDir()
	_ = os.Chmod(scratch, 0700)
	_, err = LinkScratchSkills(scratch, SkillPlan{SkillsPath: filepath.Join(root, "skills"), Required: []string{"absent"}})
	entries, _ := os.ReadDir(scratch)
	if err == nil || len(entries) != 0 {
		t.Fatal("Missing link source caused mutation")
	}
}

func TestSkillPathGrammarAndReadOnly(t *testing.T) {
	got, err := ClaudeReadRule(`/owned/[name]*?\(space here)`)
	if err != nil || got != `Read(//owned/\[name\]\*\?\\(space here)/**)` {
		t.Fatal(got, err)
	}
	for _, path := range []string{"relative", "/", "/owned\nroot", "/owned\x00root", string([]byte{'/', 0xff})} {
		if _, err := ClaudeReadRule(path); err == nil {
			t.Fatal("Invalid permission path accepted", path)
		}
	}
	for _, name := range []string{"", ".", "..", "../escape", "a/b", `a\b`, "bad\nname"} {
		if _, err := PlanSkills(SkillOptions{Provider: "codex", EffectiveRoot: t.TempDir(), Required: []string{name}}); err == nil {
			t.Fatal("Invalid skill name", name)
		}
	}
	root := t.TempDir()
	_ = os.Chmod(root, 0755)
	if _, err := LinkScratchSkills(root, SkillPlan{}); err == nil {
		t.Fatal("Nonprivate scratch accepted")
	}
	private := t.TempDir()
	_ = os.Chmod(private, 0700)
	_ = os.WriteFile(filepath.Join(private, "config.toml"), []byte("keep"), 0600)
	if _, err := LinkScratchSkills(private, SkillPlan{}); err == nil {
		t.Fatal("Nonempty scratch accepted")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	_ = os.Symlink(private, alias)
	if _, err := LinkScratchSkills(alias, SkillPlan{}); err == nil {
		t.Fatal("Symlink scratch accepted")
	}
}

func TestSkillFileLinksAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	skills := filepath.Join(root, "skills")
	dir := filepath.Join(skills, "file-linked")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := makeSkill(t, t.TempDir(), "source")
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	p, err := PlanSkills(SkillOptions{Provider: "claude", EffectiveRoot: root, Required: []string{"file-linked"}})
	physical, _ := filepath.EvalSymlinks(outside)
	if err != nil || !containsPath(p.ReadRoots, physical) {
		t.Fatal("Linked file root missing", err)
	}
	broken := filepath.Join(skills, "broken")
	_ = os.Symlink(filepath.Join(root, "absent"), broken)
	if _, err := PlanSkills(SkillOptions{Provider: "claude", EffectiveRoot: root, Required: []string{"broken"}}); err == nil {
		t.Fatal("Broken required link accepted")
	}
	if err := os.Remove(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "SKILL.md"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanSkills(SkillOptions{Provider: "claude", EffectiveRoot: root, Required: []string{"file-linked"}}); err == nil {
		t.Fatal("Nonregular skill accepted")
	}
}
