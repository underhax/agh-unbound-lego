// Package util provides general-purpose primitives used across the supervisor.
package util

import (
	"time"
)

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

// PollImmediateWithBackoff checks the condition first, then sleeps with exponentially
// increasing delay capped at maxDelay. The total wait is strictly bounded by timeout.
// Suitable for readiness probes where the service may already be up at first call.
func PollImmediateWithBackoff(timeout, initialDelay, maxDelay time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	delay := initialDelay

	for {
		if condition() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		// Cap sleep to remaining time so we never overshoot the deadline.
		time.Sleep(min(delay, remaining))
		delay = min(delay*2, maxDelay)
	}
}

// PollAfterDelayWithBackoff sleeps first, then checks the condition, with exponentially
// increasing delay capped at maxDelay. The total wait is strictly bounded by timeout.
// Suitable for post-signal termination probes where the process needs at least one
// scheduling quantum before the first check is meaningful.
func PollAfterDelayWithBackoff(timeout, initialDelay, maxDelay time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	delay := initialDelay

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		// Cap sleep to remaining time so we never overshoot the deadline.
		time.Sleep(min(delay, remaining))
		// Always check condition after waking, even if the deadline has now passed.
		// The process may have exited during the final sleep window.
		if condition() {
			return true
		}
		delay = min(delay*2, maxDelay)
	}
}
