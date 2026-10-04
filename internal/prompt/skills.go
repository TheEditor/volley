package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// SkillOptions uses an already resolved provider configuration root. It does
// not infer identity from the controller's HOME or change the child environment.
type SkillOptions struct {
	Provider, EffectiveRoot, ContextPath string
	Required                             []string
}

type SkillPlan struct {
	SkillsPath                                  string
	ReadRoots, ReadRules, Required, Unavailable []string
	SystemInstruction                           string
}

func cleanReference(path string) (string, error) {
	if !filepath.IsAbs(path) || !utf8.ValidString(path) {
		return "", fmt.Errorf("Reference path must be absolute UTF-8")
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("Reference path contains a control character")
		}
	}
	return filepath.Clean(path), nil
}

// ClaudeReadRule uses the documented // prefix for an absolute path. Escape
// literal gitignore metacharacters so a directory name cannot broaden access.
func ClaudeReadRule(path string) (string, error) {
	p, err := cleanReference(path)
	if err != nil {
		return "", err
	}
	if p == string(filepath.Separator) {
		return "", fmt.Errorf("Whole filesystem read access is not a skill rule")
	}
	var escaped strings.Builder
	for _, r := range filepath.ToSlash(p) {
		if strings.ContainsRune(`\*?[]`, r) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return "Read(/" + escaped.String() + "/**)", nil
}

func skillName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || !utf8.ValidString(name) {
		return fmt.Errorf("Invalid required skill name")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("Invalid required skill name")
		}
	}
	return nil
}

func readableSkill(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("Skill entry is not a regular file")
	}
	return nil
}

// PlanSkills records paths, not skill contents. It follows directory symlinks
// explicitly with a visited set and a bounded walk, including nested links.
// A missing requested skill fails before a caller can claim that it was used.
func PlanSkills(q SkillOptions) (SkillPlan, error) {
	p := SkillPlan{}
	if q.Provider != "claude" && q.Provider != "codex" {
		return p, fmt.Errorf("Unknown skill provider")
	}
	for _, name := range q.Required {
		if err := skillName(name); err != nil {
			return p, err
		}
	}
	p.Required = append([]string(nil), q.Required...)
	sort.Strings(p.Required)
	if q.EffectiveRoot == "" {
		p.Unavailable = []string{"Effective skill root is unknown"}
		if len(p.Required) != 0 {
			return p, fmt.Errorf("Required skills are unavailable: effective root is unknown")
		}
		return p, nil
	}
	root, err := cleanReference(q.EffectiveRoot)
	if err != nil {
		return p, err
	}
	p.SkillsPath = filepath.Join(root, "skills")
	roots := map[string]bool{p.SkillsPath: true}
	visited := map[string]bool{}
	count := 0
	var walk func(string) error
	walk = func(dir string) error {
		physical, err := filepath.EvalSymlinks(dir)
		if err != nil {
			p.Unavailable = append(p.Unavailable, dir)
			return nil
		}
		physical, err = cleanReference(physical)
		if err != nil {
			return err
		}
		roots[physical] = true
		if visited[physical] {
			return nil
		}
		visited[physical] = true
		return filepath.WalkDir(physical, func(path string, entry os.DirEntry, walkErr error) error {
			count++
			if count > 10000 {
				return fmt.Errorf("Skill reference inventory exceeds its entry limit")
			}
			if walkErr != nil {
				p.Unavailable = append(p.Unavailable, path)
				return nil
			}
			if entry.Type()&os.ModeSymlink == 0 {
				return nil
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				p.Unavailable = append(p.Unavailable, path)
				return nil
			}
			target, err = cleanReference(target)
			if err != nil {
				return err
			}
			info, err := os.Stat(target)
			if err != nil {
				p.Unavailable = append(p.Unavailable, path)
				return nil
			}
			if info.IsDir() {
				roots[target] = true
				return walk(target)
			}
			if !info.Mode().IsRegular() {
				p.Unavailable = append(p.Unavailable, path)
				return nil
			}
			roots[filepath.Dir(target)] = true
			return nil
		})
	}
	if err := walk(p.SkillsPath); err != nil {
		return p, err
	}
	var missing []string
	for _, name := range p.Required {
		if err := readableSkill(filepath.Join(p.SkillsPath, name, "SKILL.md")); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		p.Unavailable = append(p.Unavailable, missing...)
		return p, fmt.Errorf("Required skills are unavailable: %s", strings.Join(missing, ", "))
	}
	if q.ContextPath != "" {
		contextPath, err := cleanReference(q.ContextPath)
		if err != nil {
			return p, err
		}
		resolved, err := filepath.EvalSymlinks(contextPath)
		if err != nil {
			return p, fmt.Errorf("Reference context is unavailable: %w", err)
		}
		roots[contextPath] = true
		roots[resolved] = true
	}
	for r := range roots {
		p.ReadRoots = append(p.ReadRoots, r)
	}
	sort.Strings(p.ReadRoots)
	sort.Strings(p.Unavailable)
	if q.Provider == "claude" {
		for _, r := range p.ReadRoots {
			rule, err := ClaudeReadRule(r)
			if err != nil {
				return p, err
			}
			p.ReadRules = append(p.ReadRules, rule)
		}
		p.SystemInstruction = "Read required skills from " + strconv.Quote(p.SkillsPath) + ". Each skill has a SKILL.md file in its named subdirectory. Follow its instructions before applying that skill. Skill roots and reference context are read-only. Report an unavailable required skill before claiming that its standard was applied."
	}
	return p, nil
}

// LinkScratchSkills is for a caller-owned, private, empty scratch CODEX_HOME.
// Validate all required links before creating anything. Never copy config.toml.
// Creation is exclusive; a partial failure retains its owned links for diagnosis.
func LinkScratchSkills(scratch string, source SkillPlan) (SkillPlan, error) {
	p := SkillPlan{}
	root, err := cleanReference(scratch)
	if err != nil {
		return p, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return p, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return p, fmt.Errorf("Scratch skill root must be a private owned directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return p, err
	}
	if len(entries) != 0 {
		return p, fmt.Errorf("Scratch skill root must be empty")
	}
	var targets []string
	seen := map[string]bool{}
	for _, name := range source.Required {
		if err := skillName(name); err != nil {
			return p, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		target, err := filepath.EvalSymlinks(filepath.Join(source.SkillsPath, name))
		if err != nil {
			return p, err
		}
		if _, err := cleanReference(target); err != nil {
			return p, err
		}
		if err := readableSkill(filepath.Join(target, "SKILL.md")); err != nil {
			return p, err
		}
		targets = append(targets, target)
	}
	if err := os.Mkdir(filepath.Join(root, "skills"), 0700); err != nil {
		return p, err
	}
	i := 0
	for _, name := range source.Required {
		if !seen[name] {
			continue
		}
		seen[name] = false
		if err := os.Symlink(targets[i], filepath.Join(root, "skills", name)); err != nil {
			return p, err
		}
		i++
	}
	return PlanSkills(SkillOptions{Provider: "codex", EffectiveRoot: root, Required: source.Required})
}
