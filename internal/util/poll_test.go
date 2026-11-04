package util

import (
	"testing"
	"time"
)

func TestDefaultTimeFuncs(t *testing.T) {
	now := defaultNow()
	if now.IsZero() {
		t.Error("defaultNow returned zero time")
	}
	defaultSleep(1 * time.Nanosecond)
}

func TestPollImmediateWithBackoff(t *testing.T) {
	tests := []struct {
		name          string
		initialDelay  time.Duration
		maxDelay      time.Duration
		timeout       time.Duration
		successAtStep int
		want          bool
	}{
		{
			name:          "ImmediateSuccess",
			timeout:       100 * time.Millisecond,
			initialDelay:  10 * time.Millisecond,
			maxDelay:      50 * time.Millisecond,
			successAtStep: 0,
			want:          true,
		},
		{
			name:          "SuccessAfterRetries",
			timeout:       100 * time.Millisecond,
			initialDelay:  10 * time.Millisecond,
			maxDelay:      50 * time.Millisecond,
			successAtStep: 2,
			want:          true,
		},
		{
			name:          "TimeoutExpired",
			timeout:       50 * time.Millisecond,
			initialDelay:  10 * time.Millisecond,
			maxDelay:      20 * time.Millisecond,
			successAtStep: -1,
			want:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origNow := timeNow
			origSleep := timeSleep
			t.Cleanup(func() {
				timeNow = origNow
				timeSleep = origSleep
			})

			simulated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			timeNow = func() time.Time {
				return simulated
			}
			timeSleep = func(d time.Duration) {
				simulated = simulated.Add(d)
			}

			step := 0
			cond := func() bool {
				if step == tc.successAtStep {
					return true
				}
				step++
				return false
			}

			got := PollImmediateWithBackoff(tc.timeout, tc.initialDelay, tc.maxDelay, cond)
			if got != tc.want {
				t.Errorf("PollImmediateWithBackoff() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPollAfterDelayWithBackoff(t *testing.T) {
	tests := []struct {
		name          string
		initialDelay  time.Duration
		maxDelay      time.Duration
		timeout       time.Duration
		successAtStep int
		want          bool
	}{
		{
			name:          "SuccessAfterInitialDelay",
			timeout:       100 * time.Millisecond,
			initialDelay:  10 * time.Millisecond,
			maxDelay:      50 * time.Millisecond,
			successAtStep: 0,
			want:          true,
		},
		{
			name:          "SuccessAfterRetries",
			timeout:       100 * time.Millisecond,
			initialDelay:  10 * time.Millisecond,
			maxDelay:      50 * time.Millisecond,
			successAtStep: 2,
			want:          true,
		},
		{
			name:          "TimeoutExpired",
			timeout:       50 * time.Millisecond,
			initialDelay:  10 * time.Millisecond,
			maxDelay:      20 * time.Millisecond,
			successAtStep: -1,
			want:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origNow := timeNow
			origSleep := timeSleep
			t.Cleanup(func() {
				timeNow = origNow
				timeSleep = origSleep
			})

			simulated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			timeNow = func() time.Time {
				return simulated
			}
			timeSleep = func(d time.Duration) {
				simulated = simulated.Add(d)
			}

			step := 0
			cond := func() bool {
				if step == tc.successAtStep {
					return true
				}
				step++
				return false
			}

			got := PollAfterDelayWithBackoff(tc.timeout, tc.initialDelay, tc.maxDelay, cond)
			if got != tc.want {
				t.Errorf("PollAfterDelayWithBackoff() = %v, want %v", got, tc.want)
			}
		})
	}
}
