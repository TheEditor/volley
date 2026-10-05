package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheEditor/volley/internal/config"
	"github.com/TheEditor/volley/internal/contract"
	"github.com/TheEditor/volley/internal/input"
)

type parsedFlag struct {
	name, raw string
	attached  bool
	position  int
	problem   *contract.Error
}
type positionalToken struct {
	value   string
	literal bool
}

func syntax(r *contract.Registry, code, message, stage string) *contract.Error {
	e := r.Error(code, message)
	e.Stage = stage
	return e
}

// Parse performs no file, process, or settings-file access. The declaration
// supplies every flag, positional and cross-flag constraint.
func Parse(args []string, r *contract.Registry) (Invocation, error) {
	return parseWithStage(args, r, nil)
}
func parseWithStage(args []string, r *contract.Registry, stage func(string)) (Invocation, error) {
	checkStage := func(name string) {
		if stage != nil {
			stage(name)
		}
	}
	checkStage("bootstrap_mode")
	x := Invocation{Values: map[string]any{}, Settings: map[string]config.Override{}}
	global := map[string]contract.Flag{}
	all := map[string]contract.Flag{}
	for _, f := range r.GlobalFlags {
		global[f.Name] = f
		all[f.Name] = f
		for _, a := range f.Aliases {
			global[a] = f
			all[a] = f
		}
	}
	names := []string{}
	for name := range r.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, f := range r.Commands[name].Flags {
			if _, exists := all[f.Name]; !exists {
				all[f.Name] = f
			}
		}
	}
	var flags []parsedFlag
	var positions []positionalToken
	literal := false
	first := len(args)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !literal && arg == "--" {
			literal = true
			continue
		}
		if !literal && strings.HasPrefix(arg, "-") {
			name, raw, attached := strings.Cut(arg, "=")
			f, known := all[name]
			item := parsedFlag{name: name, raw: raw, attached: attached, position: i + 1}
			if known {
				item.name = f.Name
				if f.Arity == 1 && !attached {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
						item.problem = r.Error("MISSING_REQUIRED", "Missing value for "+name)
					} else {
						i++
						item.raw = args[i]
					}
				}
			}
			flags = append(flags, item)
			continue
		}
		if len(positions) == 0 {
			first = i
		}
		positions = append(positions, positionalToken{arg, literal})
	}
	checkStage("global_flags")
	// Global flags are checked before command selection, even after the verb.
	for _, item := range flags {
		if item.position <= first {
			if _, known := all[item.name]; !known {
				return x, syntax(r, "UNKNOWN_FLAG", "Unknown global flag: "+item.name, "global_flags")
			}
		}
	}
	var globalMissing *parsedFlag
	seen := map[string]bool{}
	for i := range flags {
		item := &flags[i]
		f, isGlobal := global[item.name]
		if !isGlobal {
			if item.position <= first {
				if _, known := all[item.name]; !known {
					return x, syntax(r, "UNKNOWN_FLAG", "Unknown global flag: "+item.name, "global_flags")
				}
			}
			continue
		}
		if item.problem != nil && item.problem.Code == "MISSING_REQUIRED" {
			if globalMissing == nil {
				globalMissing = item
			}
			continue
		}
		if err := validateFlag(item, f, seen, r, "global_flags"); err != nil {
			return x, err
		}
		value, err := flagValue(*item, f, r)
		if err != nil {
			if e, ok := err.(*contract.Error); ok {
				e.Stage = "global_flags"
			}
			return x, err
		}
		x.Values[f.Name] = value
	}
	checkStage("command_path")
	if len(positions) > 0 {
		first := positions[0]
		if !first.literal {
			if _, known := r.Commands[first.value]; known {
				x.Command = first.value
				positions = positions[1:]
			} else {
				compound := false
				for _, name := range names {
					if strings.HasPrefix(name, first.value+" ") {
						compound = true
						break
					}
				}
				if compound {
					if len(positions) < 2 {
						return x, r.Error("UNKNOWN_COMMAND", "Subcommand is required")
					}
					x.Command = first.value + " " + positions[1].value
					positions = positions[2:]
					if _, known := r.Commands[x.Command]; !known {
						e := r.Error("UNKNOWN_COMMAND", "Unknown command: "+x.Command)
						for _, hint := range names {
							if oneEdit(x.Command, hint) {
								h := hint
								e.DidYouMean = &h
								break
							}
						}
						return x, e
					}
				}
			}
		}
		if x.Command == "" {
			x.Command = "run"
			x.Shorthand = true
			if !first.literal {
				x.Near = nearCommand(first.value, names)
			}
		}
	}
	checkStage("local_flags")
	local := map[string]contract.Flag{}
	for _, f := range r.Commands[x.Command].Flags {
		local[f.Name] = f
	}
	for _, item := range flags {
		if _, ok := global[item.name]; ok {
			continue
		}
		if _, ok := local[item.name]; !ok {
			return x, syntax(r, "UNKNOWN_FLAG", "Unknown flag: "+item.name, "local_flags")
		}
	}
	if globalMissing != nil {
		globalMissing.problem.Stage = "types_enums"
		return x, globalMissing.problem
	}
	for i := range flags {
		item := &flags[i]
		if _, ok := global[item.name]; ok {
			continue
		}
		f, ok := local[item.name]
		if !ok {
			return x, syntax(r, "UNKNOWN_FLAG", "Unknown flag: "+item.name, "local_flags")
		}
		if err := validateFlag(item, f, seen, r, "local_flags"); err != nil {
			return x, err
		}
	}
	checkStage("types_enums")
	for _, item := range flags {
		if _, ok := global[item.name]; ok {
			continue
		}
		f := local[item.name]
		value, err := flagValue(item, f, r)
		if err != nil {
			return x, err
		}
		x.Values[f.Name] = value
		if f.Setting != "" {
			for _, s := range r.Settings {
				if s.Key == f.Setting {
					v, e := config.Validate(s, value)
					if e != nil {
						return x, r.Error("INVALID_INPUT", e.Error())
					}
					x.Settings[f.Setting] = config.Override{Value: v, Flag: f.Name, Position: item.position}
					break
				}
			}
		}
	}
	for _, p := range positions {
		if !utf8.ValidString(p.value) || strings.ContainsRune(p.value, 0) || p.value == "" {
			return x, r.Error("INVALID_INPUT", "Arguments must be nonempty UTF-8 without NUL")
		}
		x.Positionals = append(x.Positionals, p.value)
	}

	// Non-setting domains are checked before missing required selectors.
	if value, ok := x.Values["--limit"].(int); ok && (value < 1 || value > 100) {
		return x, r.Error("INVALID_INPUT", "Limit must be 1 through 100")
	}
	for _, name := range []string{"--wait-timeout", "--older-than"} {
		if value, ok := x.Values[name].(string); ok {
			duration, e := time.ParseDuration(value)
			if e != nil || duration < 0 || name == "--older-than" && duration == 0 {
				return x, r.Error("INVALID_INPUT", "Invalid duration for "+name)
			}
		}
	}
	checkStage("required_arguments")
	if x.Values["--help"] == true || x.Values["--version"] == true {
		checkStage("semantic_checks")
		return x, nil
	}
	cmd := r.Commands[x.Command]
	minimum := 0
	for _, p := range cmd.Positionals {
		if p.Required {
			minimum++
		}
	}
	if len(x.Positionals) < minimum {
		return x, r.Error("MISSING_REQUIRED", "Required positional argument is absent")
	}
	if len(x.Positionals) > len(cmd.Positionals) {
		return x, r.Error("INVALID_INPUT", "Invalid number of positional arguments")
	}
	if (x.Command == "config get" || x.Command == "config set") && len(x.Positionals) > 0 {
		key := x.Positionals[0]
		known := false
		for _, setting := range r.Settings {
			if setting.Key == key {
				known = true
				break
			}
		}
		if !known {
			return x, r.Error("INVALID_INPUT", "Unknown setting: "+key)
		}
		if x.Command == "config set" && len(x.Positionals) > 1 {
			if _, err := config.ParseArgument(key, x.Positionals[1]); err != nil {
				return x, r.Error("INVALID_INPUT", err.Error())
			}
		}
	}
	present := func(name string) bool { v, exists := x.Values[name]; return exists && v != false }
	// Required declarations first, then relationships.
	for _, c := range cmd.Constraints {
		if c["kind"] != "required" {
			continue
		}
		name, _ := c["flag"].(string)
		if !present(name) {
			code, _ := c["code"].(string)
			if code == "" {
				code = "INVALID_INPUT"
			}
			return x, r.Error(code, "Required flag: "+name)
		}
	}
	checkStage("semantic_checks")
	for _, c := range cmd.Constraints {
		flag, _ := c["flag"].(string)
		other, _ := c["other"].(string)
		kind, _ := c["kind"].(string)
		switch kind {
		case "requires":
			if present(flag) && !present(other) {
				return x, r.Error("INVALID_INPUT", flag+" requires "+other)
			}
		case "excludes":
			if present(flag) && present(other) {
				return x, r.Error("INVALID_INPUT", flag+" conflicts with "+other)
			}
		case "excludes-stdout":
			if present(flag) && present(other) && (x.Values["--deliver"] == nil || x.Values["--deliver"] == "stdout") {
				return x, r.Error("INVALID_INPUT", "Raw stdout cannot use machine rendering")
			}
		case "requires-one", "exactly-one":
			count := 0
			for _, name := range c["flags"].([]any) {
				if present(name.(string)) {
					count++
				}
			}
			if count == 0 || kind == "exactly-one" && count != 1 {
				code, _ := c["code"].(string)
				if code == "" {
					code = "INVALID_INPUT"
				}
				return x, r.Error(code, "Choose the required input selector")
			}
		}
	}
	if x.Values["--brief"] != nil && x.Values["--seed"] != nil {
		return x, r.Error("INVALID_INPUT", "Brief and seed are mutually exclusive")
	}
	if target, ok := x.Values["--deliver"].(string); ok && target != "stdout" && target != "null" && !(strings.HasPrefix(target, "file:") && len(target) > 5) {
		return x, r.Error("UNKNOWN_DELIVERY_SCHEME", "Delivery supports stdout, file:PATH, and null")
	}
	return x, nil
}
func validateFlag(item *parsedFlag, f contract.Flag, seen map[string]bool, r *contract.Registry, stage string) error {
	if item.problem != nil {
		item.problem.Stage = stage
		if item.problem.Code == "MISSING_REQUIRED" {
			item.problem.Stage = "types_enums"
		}
		return item.problem
	}
	if seen[f.Name] && !f.Repeatable {
		return syntax(r, "INVALID_INPUT", "Repeated flag: "+f.Name, stage)
	}
	seen[f.Name] = true
	if f.Arity == 0 && item.attached {
		return syntax(r, "INVALID_INPUT", f.Name+" takes no value", stage)
	}
	return nil
}
func flagValue(item parsedFlag, f contract.Flag, r *contract.Registry) (any, error) {
	if f.Arity == 0 {
		return true, nil
	}
	raw := item.raw
	if !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) || raw == "" && !f.AllowEmpty {
		return nil, r.Error("INVALID_INPUT", "Invalid value for "+f.Name)
	}
	var value any = raw
	switch f.Type {
	case "integer":
		n, e := strconv.Atoi(raw)
		if e != nil {
			return nil, r.Error("INVALID_INPUT", "Invalid integer for "+f.Name)
		}
		value = n
	case "boolean":
		if raw != "true" && raw != "false" {
			return nil, r.Error("INVALID_INPUT", "Boolean setting requires true or false")
		}
		value = raw == "true"
	case "array":
		v, e := input.JSON(strings.NewReader(raw))
		if e != nil {
			return nil, r.Error("INVALID_INPUT", e.Error())
		}
		a, ok := v.([]any)
		if !ok {
			return nil, r.Error("INVALID_INPUT", "Array flag requires a JSON array")
		}
		value = a
	}
	if len(f.Enum) > 0 {
		valid := false
		for _, choice := range f.Enum {
			if raw == choice {
				valid = true
			}
		}
		if !valid {
			e := r.Error("INVALID_INPUT", fmt.Sprintf("Invalid value for %s; allowed: %s", f.Name, strings.Join(f.Enum, ", ")))
			for _, choice := range f.Enum {
				if oneEdit(raw, choice) {
					e.DidYouMean = &choice
					break
				}
			}
			return nil, e
		}
	}
	return value, nil
}
func nearCommand(value string, names []string) string {
	tops := map[string]bool{}
	for _, name := range names {
		top := strings.Split(name, " ")[0]
		if tops[top] {
			continue
		}
		tops[top] = true
		if oneEdit(value, top) {
			return top
		}
	}
	return ""
}
func oneEdit(a, b string) bool {
	ar, br := []rune(a), []rune(b)
	if len(ar) > len(br) {
		ar, br = br, ar
	}
	if len(br)-len(ar) > 1 {
		return false
	}
	i, j, changes := 0, 0, 0
	for i < len(ar) && j < len(br) {
		if ar[i] == br[j] {
			i++
			j++
			continue
		}
		changes++
		if changes > 1 {
			return false
		}
		if len(ar) == len(br) {
			i++
		}
		j++
	}
	changes += len(br) - j
	return changes == 1
}
