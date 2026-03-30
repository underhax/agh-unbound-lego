package util

import "os"

// allowedEnvVars contains environment variables safe to pass to child processes.
var allowedEnvVars = []string{"PATH", "HOME"}

// BuildCleanEnv returns a sanitized environment for exec.Cmd.
// This prevents leaking secrets from the supervisor's environment into child processes.
func BuildCleanEnv(extraVars ...string) []string {
	env := make([]string, 0, len(allowedEnvVars)+len(extraVars))
	for _, key := range allowedEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	return append(env, extraVars...)
}
