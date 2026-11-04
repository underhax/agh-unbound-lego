package util

import (
	"slices"
	"testing"
)

func TestBuildCleanEnv(t *testing.T) {
	tests := []struct {
		name      string
		setPath   string
		setHome   string
		extra     []string
		wantExist []string
		wantLack  []string
	}{
		{
			name:      "AllSetWithExtra",
			setPath:   "/usr/bin",
			setHome:   "/home/user",
			extra:     []string{"CUSTOM_VAR=value"},
			wantExist: []string{"PATH=/usr/bin", "HOME=/home/user", "CUSTOM_VAR=value"},
			wantLack:  nil,
		},
		{
			name:      "HomeUnset",
			setPath:   "/bin",
			setHome:   "",
			extra:     []string{"OTHER=1"},
			wantExist: []string{"PATH=/bin", "OTHER=1"},
			wantLack:  []string{"HOME="},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setPath != "" {
				t.Setenv("PATH", tc.setPath)
			}
			if tc.setHome != "" {
				t.Setenv("HOME", tc.setHome)
			} else {
				t.Setenv("HOME", "")
			}

			env := BuildCleanEnv(tc.extra...)

			for _, expected := range tc.wantExist {
				if !slices.Contains(env, expected) {
					t.Errorf("expected %s in env, got %v", expected, env)
				}
			}

			for _, unexpected := range tc.wantLack {
				for _, entry := range env {
					if entry == unexpected {
						t.Errorf("did not expect %s in env", entry)
					}
				}
			}
		})
	}
}
