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
)

// DefaultSecretsDir points to the standard location for Docker Swarm/Compose secrets.
const DefaultSecretsDir = "/run/secrets"

// Secret wraps sensitive strings to prevent accidental logging via %v or %+v.
type Secret string

func (s Secret) String() string { return "***REDACTED***" }

// Config holds the validated environment and secret parameters.
type Config struct {
	ACMEDomain string
	ACMEEmail  Secret
	CFDNSToken Secret
	LogLevel   slog.Level
	DisableECN bool
	LegoEnable bool
}

// Load reads configuration from the environment and secrets directory.
// It fails fast if required configurations or secrets are missing.
func Load(secretsDir string) (*Config, error) {
	rawLogLevel := os.Getenv("LOG_LEVEL")
	if rawLogLevel == "" {
		rawLogLevel = "info"
	}

	var logLevel slog.Level
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
		domain, email, cfToken, err = readLegoSecrets(secretsDir)
		if err != nil {
			return nil, err
		}
	}

	return &Config{
		ACMEDomain: domain,
		LogLevel:   logLevel,
		DisableECN: disableECN,
		ACMEEmail:  Secret(email),
		CFDNSToken: Secret(cfToken),
		LegoEnable: legoEnable,
	}, nil
}

func readLegoSecrets(secretsDir string) (domain, email, cfToken string, err error) {
	domain, err = readSecret(secretsDir, "acme_domain")
	if err != nil {
		return "", "", "", err
	}

	if err = validateDomain(domain); err != nil {
		return "", "", "", fmt.Errorf("invalid acme_domain: %w", err)
	}

	email, err = readSecret(secretsDir, "acme_email")
	if err != nil {
		return "", "", "", err
	}

	if err = validateEmail(email); err != nil {
		return "", "", "", fmt.Errorf("invalid acme_email: %w", err)
	}

	cfToken, err = readSecret(secretsDir, "cf_dns_api_token")
	if err != nil {
		return "", "", "", err
	}

	return domain, email, cfToken, nil
}

func readSecret(dir, filename string) (string, error) {
	const maxSecretSize = 4096

	path := filepath.Clean(filepath.Join(dir, filename))

	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid secret path: %s", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("failed to stat secret '%s': %w", filename, err)
	}

	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("secret '%s' is not a regular file", filename)
	}

	if info.Size() > maxSecretSize {
		return "", fmt.Errorf("secret '%s' exceeds %d bytes", filename, maxSecretSize)
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

func validateDomainLabel(label string) error {
	if label == "" || len(label) > 63 {
		return errors.New("domain label length must be between 1 and 63 characters")
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return errors.New("domain label cannot start or end with a hyphen")
	}
	for _, c := range label {
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '-' {
			return fmt.Errorf("invalid character '%c' in domain label", c)
		}
	}
	return nil
}

// validateDomain strictly validates a domain per RFC 1123 / RFC 952.
func validateDomain(domain string) error {
	if domain == "" {
		return errors.New("acme_domain is required when LEGO_ENABLE is true")
	}
	if len(domain) > 253 {
		return errors.New("domain name exceeds maximum length of 253 characters")
	}

	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return errors.New("domain must contain at least two labels")
	}

	for _, label := range labels {
		if err := validateDomainLabel(label); err != nil {
			return err
		}
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
