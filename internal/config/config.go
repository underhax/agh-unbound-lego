// Package config handles application environment variables and Docker secrets.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// DefaultSecretsDir points to the standard location for Docker Swarm/Compose secrets.
const DefaultSecretsDir = "/run/secrets"

// Config holds the validated environment and secret parameters.
type Config struct {
	ACMEDomain string
	ACMEEmail  string
	CFDNSToken string
	LogLevel   slog.Level
	DisableECN bool
	LegoEnable bool
}

// Load reads configuration from the environment and secrets directory.
// It fails fast if required configurations or secrets are missing.
func Load(secretsDir string) (*Config, error) {
	rawLogLevel := os.Getenv("LOG_LEVEL")
	if rawLogLevel == "" {
		rawLogLevel = "info" // Default safe level
	}

	var logLevel slog.Level
	// Strict validation prevents silent fallbacks masking configuration drifts.
	switch strings.ToLower(rawLogLevel) {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid LOG_LEVEL provided: %s", rawLogLevel)
	}

	var legoEnable, disableECN bool
	var err error

	if val := os.Getenv("LEGO_ENABLE"); val != "" {
		if legoEnable, err = strconv.ParseBool(val); err != nil {
			return nil, fmt.Errorf("invalid LEGO_ENABLE boolean value: %w", err)
		}
	}

	if val := os.Getenv("QUIC_GO_DISABLE_ECN"); val != "" {
		if disableECN, err = strconv.ParseBool(val); err != nil {
			return nil, fmt.Errorf("invalid QUIC_GO_DISABLE_ECN boolean value: %w", err)
		}
	}

	var domain, email, cfToken string

	if legoEnable {
		domain = os.Getenv("ACME_DOMAIN")
		if err = validateDomain(domain); err != nil {
			return nil, fmt.Errorf("invalid ACME_DOMAIN: %w", err)
		}

		email, err = readSecret(secretsDir, "acme_email")
		if err != nil {
			return nil, err
		}

		if err = validateEmail(email); err != nil {
			return nil, fmt.Errorf("invalid ACME_EMAIL: %w", err)
		}

		cfToken, err = readSecret(secretsDir, "cf_dns_api_token")
		if err != nil {
			return nil, err
		}
	}

	return &Config{
		ACMEDomain: domain,
		LogLevel:   logLevel,
		DisableECN: disableECN,
		ACMEEmail:  email,
		CFDNSToken: cfToken,
		LegoEnable: legoEnable,
	}, nil
}

// readSecret securely reads a secret from a file mounted by Docker.
// Trims whitespace/newlines to prevent unexpected authentication failures.
func readSecret(dir, filename string) (string, error) {
	path := filepath.Clean(filepath.Join(dir, filename))

	// Prevent directory traversal attacks if filename was ever dynamic
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid secret path: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read secret '%s': %w", filename, err)
	}

	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("secret '%s' is empty", filename)
	}

	return secret, nil
}

// validateDomain strictly validates and normalizes a domain using the official IDNA profile.
func validateDomain(domain string) error {
	if _, err := idna.Lookup.ToASCII(domain); err != nil {
		return fmt.Errorf("IDNA validation failed: %w", err)
	}
	return nil
}

// validateEmail ensures the email has a valid structure and a compliant IDNA domain.
func validateEmail(email string) error {
	parts := strings.Split(email, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errors.New("missing local or domain part")
	}
	return validateDomain(parts[1])
}
