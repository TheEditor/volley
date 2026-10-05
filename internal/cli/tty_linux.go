package cli

import "golang.org/x/sys/unix"

func terminalRequest() uint { return unix.TCGETS }
