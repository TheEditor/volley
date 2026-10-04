//go:build darwin

package process

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func platformProcesses() ([]observation, error) {
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return nil, err
	}
	records, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]observation, 0, len(records))
	for _, p := range records {
		out = append(out, observation{Identity: Identity{int(p.Proc.P_pid), int(p.Eproc.Pgid), fmt.Sprintf("%d:%d/%d:%d", boot.Sec, boot.Usec, p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec), true}, Parent: int(p.Eproc.Ppid), Zombie: p.Proc.P_stat == 5})
	}
	return out, nil
}

func IsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA)
	return err == nil
}
