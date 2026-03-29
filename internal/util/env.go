package util

import "os"

// AllowedEnvVars contains environment variables safe to pass to child processes.
var AllowedEnvVars = []string{"PATH", "HOME"}

// BuildCleanEnv returns a sanitized environment for exec.Cmd.
// This prevents leaking secrets from the supervisor's environment into child processes.
func BuildCleanEnv(extraVars ...string) []string {
	env := make([]string, 0, len(AllowedEnvVars)+len(extraVars))
	for _, key := range AllowedEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return append(env, extraVars...)
}
