package conformance

import (
	"fmt"
	"github.com/TheEditor/volley/internal/contract"
	"sort"
	"strings"
)

func Generate(r *contract.Registry, full bool) []Case {
	cases := []Case{}
	add := func(id string, t Target, args []string, code string) {
		exit := 0
		if code != "" {
			exit = r.Error(code, "").Exit
		}
		cases = append(cases, Case{ID: id, Target: t, Args: args, Want: Observation{Exit: exit, OK: code == "", Code: code}})
	}
	names := []string{}
	for name := range r.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cmd := r.Commands[name]
		base := strings.Fields(name)
		help := append(append([]string{}, base...), "--help")
		add("B-03::verb="+name, Target{Verb: name, Stage: "local_flags"}, append(append([]string{}, help...), "--v0ll3y-unknown"), "UNKNOWN_FLAG")
		add("H-03::verb="+name, Target{Verb: name, Shape: "envelope"}, help, "")
		for _, mode := range cmd.OutputModes {
			if mode == "raw" {
				cases = append(cases, Case{ID: "M-01::verb=" + name + "::shape=" + mode, Target: Target{Verb: name, Shape: mode}, Args: append(append([]string{}, base...), "--toml", "--deliver=null"), Want: Observation{OK: true}})
			} else {
				add("M-01::verb="+name+"::shape="+mode, Target{Verb: name, Shape: mode}, help, "")
			}
		}
		flags := cmd.Flags
		if name == "capabilities" {
			flags = append(append([]contract.Flag{}, r.GlobalFlags...), flags...)
		}
		for _, f := range flags {
			flagBase := help
			if f.Name == "--help" {
				flagBase = base
			}
			value := valueFor(f)
			valid := func(spelling, form string) []string {
				args := append([]string{}, flagBase...)
				if f.Arity == 0 {
					return append(args, spelling)
				}
				if form == "equals" {
					return append(args, spelling+"="+value)
				}
				return append(args, spelling, value)
			}
			target := Target{Verb: name, Flag: f.Name, Stage: "types_enums"}
			for _, spelling := range append([]string{f.Name}, f.Aliases...) {
				for _, form := range []string{"space", "equals"} {
					if f.Arity == 0 && form == "equals" {
						continue
					}
					target.Flag = spelling
					add("B-valid::verb="+name+"::flag="+spelling+"::form="+form, target, valid(spelling, form), "")
				}
			}
			target.Flag = f.Name
			if !f.Repeatable {
				args := valid(f.Name, "space")
				if f.Arity == 0 {
					args = append(args, f.Name)
				} else {
					args = append(args, f.Name, value)
				}
				add("B-repeat::verb="+name+"::flag="+f.Name, target, args, "INVALID_INPUT")
			}
			if f.Arity == 0 {
				add("M-03::verb="+name+"::flag="+f.Name, target, append(append([]string{}, flagBase...), f.Name+"=false"), "INVALID_INPUT")
				continue
			}
			add("B-07::verb="+name+"::flag="+f.Name, target, append(append([]string{}, flagBase...), f.Name), "MISSING_REQUIRED")
			add("M-05::verb="+name+"::flag="+f.Name, target, append(append([]string{}, base...), f.Name, "--json"), "MISSING_REQUIRED")
			add("B-08::verb="+name+"::flag="+f.Name, target, append(append([]string{}, base...), f.Name, "--help"), "MISSING_REQUIRED")
			add("B-09::verb="+name+"::flag="+f.Name, target, append(append([]string{}, base...), f.Name, "--v0ll3y-unknown"), "UNKNOWN_FLAG")
			code := "INVALID_INPUT"
			if f.AllowEmpty {
				code = ""
			}
			add("B-10::verb="+name+"::flag="+f.Name, target, append(append([]string{}, flagBase...), f.Name+"="), code)
			for _, setting := range r.Settings {
				if setting.Key == f.Setting && setting.Validation["identifier"] == true {
					add("B-11::verb="+name+"::flag="+f.Name, target, append(append([]string{}, flagBase...), f.Name+"=-leading-dash"), "INVALID_INPUT")
				}
			}
			if len(f.Enum) > 0 {
				add("B-12::verb="+name+"::flag="+f.Name, target, append(append([]string{}, flagBase...), f.Name+"="+f.Enum[0]+"x"), "INVALID_INPUT")
				cases[len(cases)-1].Hint = true
			}
		}
		for i, p := range cmd.Positionals {
			args := append([]string{}, help...)
			for j := 0; j < i; j++ {
				args = append(args, "fixture")
			}
			args = append(args, "--", "--json")
			if name == "feedback" {
				args = append(append([]string{}, base...), "--", "--json")
			}
			add("B-13::verb="+name+"::node="+p.Name, Target{Verb: name, Node: p.Name, Stage: "types_enums"}, args, "")
			args = append([]string{}, help...)
			args = append(args, "")
			add("B-10::verb="+name+"::node="+p.Name, Target{Verb: name, Node: p.Name, Stage: "types_enums"}, args, "INVALID_INPUT")
		}
	}
	add("B-01::node=runs", Target{Node: "runs", Stage: "command_path"}, []string{"runs", "v0ll3y-unknown"}, "UNKNOWN_COMMAND")
	add("B-04", Target{Stage: "global_flags"}, []string{"--v0ll3y-unknown", "capabilities"}, "UNKNOWN_FLAG")
	add("B-05", Target{Stage: "global_flags"}, []string{"--v0ll3y-unknown", "runs", "v0ll3y-unknown"}, "UNKNOWN_FLAG")
	add("B-06", Target{Verb: "runs get", Stage: "local_flags"}, []string{"runs", "get", "absent-owned-resource", "--v0ll3y-unknown"}, "UNKNOWN_FLAG")
	add("B-02::node=runs", Target{Node: "runs", Stage: "command_path"}, []string{"runs", "lisx"}, "UNKNOWN_COMMAND")
	cases[len(cases)-1].Hint = true
	add("M-03", Target{Shape: "envelope"}, []string{"--json=false"}, "INVALID_INPUT")
	add("M-07::raw=json", Target{Verb: "config show", Shape: "raw"}, []string{"config", "show", "--toml", "--json"}, "INVALID_INPUT")
	for _, probe := range []string{"M-06", "X-06::streams"} {
		cases = append(cases, Case{ID: probe, Target: Target{Shape: "frames"}, Unavailable: "streams-deferred"})
	}
	for _, args := range [][]string{nil, {"--help"}, {"--version"}, {"--help", "--", "--json"}} {
		id := "H-02::" + strings.Join(args, "/")
		cases = append(cases, Case{ID: id, Target: Target{Shape: "human"}, Args: args, Human: true, Want: Observation{OK: true}})
	}
	add("H-03::shape=bare", Target{Shape: "envelope"}, nil, "")
	add("H-03::shape=version", Target{Shape: "envelope"}, []string{"--version"}, "")
	cases = append(cases, Case{ID: "M-02", Human: true, Args: []string{"--v0ll3y-unknown", "capabilities"}, Want: Observation{Exit: 1, Code: "UNKNOWN_FLAG"}, Target: Target{Shape: "human"}})
	for _, name := range names {
		c := r.Commands[name]
		if name == "feedback" && len(c.Positionals) > 0 {
			add("H-04::verb="+name, Target{Verb: name, Node: c.Positionals[0].Name}, []string{name, "--", "--help"}, "")
			cases = append(cases, Case{ID: "M-04::verb=" + name, Target: Target{Verb: name, Node: c.Positionals[0].Name, Shape: "human"}, Args: []string{name, "--", "--json"}, Human: true, Want: Observation{OK: true}})
		}
	}
	stages := append([]string{"entry"}, r.DiagnosisOrder...)
	for _, stage := range stages {
		c := Case{ID: func() string {
			if stage == "entry" {
				return "S-02::stage=entry"
			}
			return "S-01::stage=" + stage
		}(), Target: Target{Stage: stage, Shape: "envelope"}, Args: []string{"--help"}, Fault: stage, Want: Observation{Exit: 6, Code: "INTERNAL"}}
		if !full {
			c.Unavailable = "release-build-fault-trigger-unavailable"
		}
		cases = append(cases, c)
	}
	if !full {
		cases = append(cases, Case{ID: "X-03", Target: Target{Stage: "release"}, Want: Observation{OK: true}})
	}
	for i := range cases {
		parts := strings.Split(cases[i].ID, "::")
		sort.Strings(parts[1:])
		cases[i].ID = strings.Join(parts, "::")
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases
}
func valueFor(f contract.Flag) string {
	if len(f.Enum) > 0 {
		for _, v := range f.Enum {
			if v != "" || f.AllowEmpty {
				return v
			}
		}
	}
	if f.Default != nil {
		b, err := contract.Canonical(f.Default)
		if err == nil {
			if s, ok := f.Default.(string); ok {
				if s != "" || f.AllowEmpty {
					return s
				}
			} else {
				return string(b)
			}
		}
	}
	switch f.Type {
	case "duration":
		return "1s"
	case "integer":
		return "1"
	case "boolean":
		return "false"
	case "array":
		return "[]"
	}
	if f.Name == "--deliver" {
		return "stdout"
	}
	return fmt.Sprint("fixture")
}
