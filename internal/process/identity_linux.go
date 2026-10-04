//go:build linux

package process

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func platformProcesses() ([]observation, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]observation, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// comm can contain spaces and parentheses. Fields after its last closing
		// parenthesis start at field 3 (state); starttime is field 22.
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			return nil, fmt.Errorf("Invalid process stat")
		}
		fields := strings.Fields(string(raw[end+1:]))
		if len(fields) < 20 {
			return nil, fmt.Errorf("Short process stat")
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		pgid, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, err
		}
		if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
			return nil, err
		}
		out = append(out, observation{Identity: Identity{pid, pgid, strings.TrimSpace(string(boot)) + "/" + fields[19], true}, Parent: parent, Zombie: fields[0] == "Z"})
	}
	return out, nil
}
