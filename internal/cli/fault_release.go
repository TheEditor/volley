//go:build !volleytest

package cli

func injectFault(string, []string)           {}
func fullCI() bool                           { return false }
func testEnvironmentReads() []map[string]any { return nil }

func unsupportedProcessEnvironment([]string) bool { return false }
func conformanceDefect([]string) bool             { return false }
