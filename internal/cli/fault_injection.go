//go:build volleytest

package cli

func injectFault(stage string, env []string) {
	if envValue(env, "VOLLEY_TEST_FAULT") == stage {
		panic("owned conformance fault")
	}
}
func fullCI() bool { return true }
func testEnvironmentReads() []map[string]any {
	return []map[string]any{{"name": "VOLLEY_TEST_FAULT", "purpose": "test-build fault injection only", "scope": "test-only"}}
}

func unsupportedProcessEnvironment(env []string) bool {
	return envValue(env, "VOLLEY_TEST_FAULT") == "platform"
}
func conformanceDefect(env []string) bool { return envValue(env, "VOLLEY_TEST_FAULT") == "conformance" }
