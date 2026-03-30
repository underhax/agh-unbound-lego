package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_Success(t *testing.T) {
	t.Setenv("ACME_DOMAIN", "dns.example.tld")
	t.Setenv("LEGO_ENABLE", "true")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("QUIC_GO_DISABLE_ECN", "true")

	secretsDir := t.TempDir()

	err := os.WriteFile(filepath.Join(secretsDir, "acme_email"), []byte("admin@example.tld\n"), 0o600)
	if err != nil {
		t.Fatalf("Failed to write mock email secret: %v", err)
	}

	err = os.WriteFile(filepath.Join(secretsDir, "cf_dns_api_token"), []byte("supersecrettoken"), 0o600)
	if err != nil {
		t.Fatalf("Failed to write mock token secret: %v", err)
	}

	cfg, err := Load(secretsDir)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if cfg.ACMEDomain != "dns.example.tld" {
		t.Errorf("Expected domain dns.example.tld, got %s", cfg.ACMEDomain)
	}
	if cfg.ACMEEmail != "admin@example.tld" {
		t.Errorf("Expected email admin@example.tld, got %s", cfg.ACMEEmail) // Trimming verified
	}
	_ = string(cfg.ACMEEmail)
	if !cfg.DisableECN {
		t.Errorf("Expected DisableECN to be true")
	}
	if !cfg.LegoEnable {
		t.Errorf("Expected LegoEnable to be true")
	}
}

func TestLoadConfig_InvalidData(t *testing.T) {
	t.Run("InvalidDomain", func(t *testing.T) {
		t.Setenv("ACME_DOMAIN", "invalid_domain!")
		t.Setenv("LEGO_ENABLE", "true")
		if _, err := Load(t.TempDir()); err == nil {
			t.Error("Expected error for invalid IDNA domain, got nil")
		}
	})

	t.Run("InvalidEmail", func(t *testing.T) {
		t.Setenv("ACME_DOMAIN", "dns.example.tld")
		t.Setenv("LEGO_ENABLE", "true")
		secretsDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(secretsDir, "acme_email"), []byte("invalid-email-format\n"), 0o600); err != nil {
			t.Fatalf("Failed to write mock email secret: %v", err)
		}
		if err := os.WriteFile(filepath.Join(secretsDir, "cf_dns_api_token"), []byte("token"), 0o600); err != nil {
			t.Fatalf("Failed to write mock token secret: %v", err)
		}

		if _, err := Load(secretsDir); err == nil {
			t.Error("Expected error for invalid email structure, got nil")
		}
	})
}

func TestLoadConfig_MissingEnv(t *testing.T) {
	t.Setenv("ACME_DOMAIN", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("QUIC_GO_DISABLE_ECN", "")
	t.Setenv("LEGO_ENABLE", "true")

	_, err := Load(t.TempDir())
	if err == nil {
		t.Fatal("Expected error for missing ACME_DOMAIN, got nil")
	}
}

func TestLoadConfig_LegoDisabled(t *testing.T) {
	t.Setenv("ACME_DOMAIN", "")
	t.Setenv("LEGO_ENABLE", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("QUIC_GO_DISABLE_ECN", "")

	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Expected no error when Lego is disabled, got: %v", err)
	}

	if cfg.LegoEnable {
		t.Errorf("Expected LegoEnable to be false")
	}
}
