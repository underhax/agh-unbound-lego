package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecret_String(t *testing.T) {
	s := Secret("super-confidential-token")
	if s.String() != "***REDACTED***" {
		t.Errorf("Secret.String() leaked contents: %s", s.String())
	}
}

func TestLoadConfig_LogLevels(t *testing.T) {
	tests := []struct {
		name      string
		rawLevel  string
		wantLevel slog.Level
		wantErr   bool
	}{
		{
			name:      "DebugLevel",
			rawLevel:  "debug",
			wantLevel: slog.LevelDebug,
			wantErr:   false,
		},
		{
			name:      "InfoLevel",
			rawLevel:  "info",
			wantLevel: slog.LevelInfo,
			wantErr:   false,
		},
		{
			name:      "WarnLevel",
			rawLevel:  "warn",
			wantLevel: slog.LevelWarn,
			wantErr:   false,
		},
		{
			name:      "ErrorLevel",
			rawLevel:  "error",
			wantLevel: slog.LevelError,
			wantErr:   false,
		},
		{
			name:      "DefaultLevelWhenEmpty",
			rawLevel:  "",
			wantLevel: slog.LevelInfo,
			wantErr:   false,
		},
		{
			name:      "InvalidLevelFails",
			rawLevel:  "verbose",
			wantLevel: slog.LevelInfo,
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", tc.rawLevel)
			t.Setenv("LEGO_ENABLE", "false")

			cfg, err := Load(t.TempDir())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && cfg.LogLevel != tc.wantLevel {
				t.Errorf("got level %v, want %v", cfg.LogLevel, tc.wantLevel)
			}
		})
	}
}

func TestLoadConfig_BooleanParsing(t *testing.T) {
	tests := []struct {
		name       string
		legoVal    string
		quicVal    string
		wantErr    bool
		wantLego   bool
		wantQuicEC bool
	}{
		{
			name:       "ValidBooleans",
			legoVal:    "0",
			quicVal:    "1",
			wantErr:    false,
			wantLego:   false,
			wantQuicEC: true,
		},
		{
			name:    "InvalidLegoBool",
			legoVal: "not_a_bool",
			quicVal: "false",
			wantErr: true,
		},
		{
			name:    "InvalidQuicBool",
			legoVal: "f",
			quicVal: "invalid_truth",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEGO_ENABLE", tc.legoVal)
			t.Setenv("QUIC_GO_DISABLE_ECN", tc.quicVal)

			cfg, err := Load(t.TempDir())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if cfg.LegoEnable != tc.wantLego {
					t.Errorf("got LegoEnable %v, want %v", cfg.LegoEnable, tc.wantLego)
				}
				if cfg.DisableECN != tc.wantQuicEC {
					t.Errorf("got DisableECN %v, want %v", cfg.DisableECN, tc.wantQuicEC)
				}
			}
		})
	}
}

func writeSecretFile(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write secret %s: %v", filename, err)
	}
}

func TestLoadConfig_LegoIntegration(t *testing.T) {
	tests := []struct {
		setupSecrets func(t *testing.T, dir string)
		name         string
		wantErr      bool
	}{
		{
			setupSecrets: func(t *testing.T, dir string) {
				writeSecretFile(t, dir, "acme_domain", "dns.example.com\n")
				writeSecretFile(t, dir, "acme_email", "admin@example.com\n")
				writeSecretFile(t, dir, "cf_dns_api_token", "api-token-123\n")
			},
			name:    "FullValidConfig",
			wantErr: false,
		},
		{
			setupSecrets: func(t *testing.T, dir string) {
				writeSecretFile(t, dir, "acme_email", "admin@example.com")
				writeSecretFile(t, dir, "cf_dns_api_token", "token")
			},
			name:    "MissingDomainSecretFails",
			wantErr: true,
		},
		{
			setupSecrets: func(t *testing.T, dir string) {
				writeSecretFile(t, dir, "acme_domain", "invalid_domain!")
				writeSecretFile(t, dir, "acme_email", "admin@example.com")
				writeSecretFile(t, dir, "cf_dns_api_token", "token")
			},
			name:    "InvalidDomainSecretFails",
			wantErr: true,
		},
		{
			setupSecrets: func(t *testing.T, dir string) {
				writeSecretFile(t, dir, "acme_domain", "dns.example.com")
				writeSecretFile(t, dir, "cf_dns_api_token", "token")
			},
			name:    "MissingEmailSecretFails",
			wantErr: true,
		},
		{
			setupSecrets: func(t *testing.T, dir string) {
				writeSecretFile(t, dir, "acme_domain", "dns.example.com")
				writeSecretFile(t, dir, "acme_email", "notanemail")
				writeSecretFile(t, dir, "cf_dns_api_token", "token")
			},
			name:    "InvalidEmailSecretFails",
			wantErr: true,
		},
		{
			setupSecrets: func(t *testing.T, dir string) {
				writeSecretFile(t, dir, "acme_domain", "dns.example.com")
				writeSecretFile(t, dir, "acme_email", "admin@example.com")
			},
			name:    "MissingTokenSecretFails",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEGO_ENABLE", "true")
			dir := t.TempDir()
			tc.setupSecrets(t, dir)
			cfg, err := Load(dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if cfg.ACMEDomain != "dns.example.com" {
					t.Errorf("domain mismatch: got %s", cfg.ACMEDomain)
				}
				if string(cfg.ACMEEmail) != "admin@example.com" {
					t.Errorf("email mismatch: got %s", cfg.ACMEEmail)
				}
				if string(cfg.CFDNSToken) != "api-token-123" {
					t.Errorf("token mismatch: got %s", cfg.CFDNSToken)
				}
			}
		})
	}
}

func TestValidateDomain(t *testing.T) {
	tests := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{
			name:    "ValidStandardDomain",
			domain:  "dns.example.com",
			wantErr: false,
		},
		{
			name:    "ValidSubdomainHierarchy",
			domain:  "deep.resolver.example.org",
			wantErr: false,
		},
		{
			name:    "EmptyDomain",
			domain:  "",
			wantErr: true,
		},
		{
			name:    "SingleLabelOnly",
			domain:  "localhost",
			wantErr: true,
		},
		{
			name:    "ExceedsMaxTotalLength",
			domain:  strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".example.com",
			wantErr: true,
		},
		{
			name:    "LabelExceedsMaxLabelLength",
			domain:  strings.Repeat("a", 64) + ".example.com",
			wantErr: true,
		},
		{
			name:    "EmptyLabelDoubleDot",
			domain:  "dns..example.com",
			wantErr: true,
		},
		{
			name:    "LeadingHyphenInLabel",
			domain:  "-dns.example.com",
			wantErr: true,
		},
		{
			name:    "TrailingHyphenInLabel",
			domain:  "dns-.example.com",
			wantErr: true,
		},
		{
			name:    "InvalidCharactersInLabel",
			domain:  "dns_query.example.com",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDomain(tc.domain)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateDomain(%q) error = %v, wantErr %v", tc.domain, err, tc.wantErr)
			}
		})
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		wantErr bool
	}{
		{
			name:    "ValidEmail",
			email:   "admin@example.com",
			wantErr: false,
		},
		{
			name:    "MissingAtSymbol",
			email:   "adminexample.com",
			wantErr: true,
		},
		{
			name:    "MissingLocalPart",
			email:   "@example.com",
			wantErr: true,
		},
		{
			name:    "MissingDomainPart",
			email:   "admin@",
			wantErr: true,
		},
		{
			name:    "MultipleAtSymbols",
			email:   "admin@sub@example.com",
			wantErr: true,
		},
		{
			name:    "InvalidDomainInEmail",
			email:   "admin@invalid_domain.example.com",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEmail(tc.email)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateEmail(%q) error = %v, wantErr %v", tc.email, err, tc.wantErr)
			}
		})
	}
}

func TestReadSecret(t *testing.T) {
	tempDir := t.TempDir()

	validSecret := filepath.Join(tempDir, "valid")
	if err := os.WriteFile(validSecret, []byte("  secret_value \n"), 0o600); err != nil {
		t.Fatalf("failed to write valid secret: %v", err)
	}

	emptySecret := filepath.Join(tempDir, "empty")
	if err := os.WriteFile(emptySecret, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("failed to write empty secret: %v", err)
	}

	hugeSecret := filepath.Join(tempDir, "huge")
	if err := os.WriteFile(hugeSecret, []byte(strings.Repeat("x", 4097)), 0o600); err != nil {
		t.Fatalf("failed to write huge secret: %v", err)
	}

	subDir := filepath.Join(tempDir, "subdir")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	unreadableSecret := filepath.Join(tempDir, "unreadable")
	if err := os.WriteFile(unreadableSecret, []byte("secret"), 0o000); err != nil {
		t.Fatalf("failed to write unreadable secret: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(unreadableSecret, 0o600); err != nil {
			t.Logf("failed to restore file permission: %v", err)
		}
	})

	tests := []struct {
		name     string
		dir      string
		filename string
		want     string
		wantErr  bool
	}{
		{
			name:     "SuccessTrimmed",
			dir:      tempDir,
			filename: "valid",
			want:     "secret_value",
			wantErr:  false,
		},
		{
			name:     "EmptySecretError",
			dir:      tempDir,
			filename: "empty",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "ExceedsMaxSizeError",
			dir:      tempDir,
			filename: "huge",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "NonRegularFileError",
			dir:      tempDir,
			filename: "subdir",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "UnreadableFileError",
			dir:      tempDir,
			filename: "unreadable",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "MissingSecretFile",
			dir:      tempDir,
			filename: "missing",
			want:     "",
			wantErr:  true,
		},
		{
			name:     "DirectoryTraversalPath",
			dir:      tempDir,
			filename: "../traversal",
			want:     "",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readSecret(tc.dir, tc.filename)
			if (err != nil) != tc.wantErr {
				t.Fatalf("readSecret() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("readSecret() = %q, want %q", got, tc.want)
			}
		})
	}
}
