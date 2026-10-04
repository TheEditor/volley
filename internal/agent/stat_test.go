//go:build darwin || linux

package agent

import (
	"os"
	"syscall"
)

func statIdentity(info os.FileInfo) [2]uint64 {
	st := info.Sys().(*syscall.Stat_t)
	return [2]uint64{uint64(st.Dev), uint64(st.Ino)}
}
