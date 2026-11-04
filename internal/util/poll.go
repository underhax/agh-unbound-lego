// Package util provides general-purpose primitives used across the supervisor.
package util

import (
	"time"
)

func defaultSleep(d time.Duration) {
	time.Sleep(d)
}

func defaultNow() time.Time {
	return time.Now()
}

var (
	timeSleep = defaultSleep
	timeNow   = defaultNow
)

// PollImmediateWithBackoff loops with exponentially increasing delay capped at maxDelay.
// The total wait is strictly bounded by timeout. Suitable for readiness probes where
// the service may already be up at the first call.
func PollImmediateWithBackoff(timeout, initialDelay, maxDelay time.Duration, condition func() bool) bool {
	deadline := timeNow().Add(timeout)
	delay := initialDelay

	for {
		if condition() {
			return true
		}
		remaining := deadline.Sub(timeNow())
		if remaining <= 0 {
			return false
		}
		timeSleep(min(delay, remaining))
		delay = min(delay*2, maxDelay)
	}
}

// PollAfterDelayWithBackoff loops with exponentially increasing delay capped at maxDelay.
// The total wait is strictly bounded by timeout. Suitable for post-signal termination
// probes where the process needs at least one scheduling quantum before the first check
// is meaningful.
func PollAfterDelayWithBackoff(timeout, initialDelay, maxDelay time.Duration, condition func() bool) bool {
	deadline := timeNow().Add(timeout)
	delay := initialDelay

	for {
		remaining := deadline.Sub(timeNow())
		if remaining <= 0 {
			return false
		}
		timeSleep(min(delay, remaining))
		if condition() {
			return true
		}
		delay = min(delay*2, maxDelay)
	}
}
