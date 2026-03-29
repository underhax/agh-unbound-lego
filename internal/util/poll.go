// Package util provides general-purpose primitives used across the supervisor.
package util

import "time"

// PollImmediate checks the condition first, then sleeps. Suitable for readiness probes
// where the condition may already be true at the time of the first call.
func PollImmediate(attempts int, delay time.Duration, condition func() bool) bool {
	for range attempts {
		if condition() {
			return true
		}
		time.Sleep(delay)
	}
	return false
}

// PollAfterDelay sleeps before each check. Suitable for post-signal termination probes
// where the target process needs time to handle the signal before the first check is meaningful.
func PollAfterDelay(attempts int, delay time.Duration, condition func() bool) bool {
	for range attempts {
		time.Sleep(delay)
		if condition() {
			return true
		}
	}
	return false
}
