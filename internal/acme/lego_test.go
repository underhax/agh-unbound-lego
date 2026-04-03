package acme

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/underhax/agh-unbound-lego/internal/config"
)

func TestBuildCmd_WildcardAndEnv(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ACMEDomain: "dns.example.tld",
		ACMEEmail:  config.Secret("admin@test.local"),
		CFDNSToken: config.Secret("secret-token"),
		LegoEnable: true,
	}
	m := NewManager(cfg, nil)

	cmd := m.buildCmd(context.Background(), "run")
	args := strings.Join(cmd.Args, " ")

	if !strings.Contains(args, "--domains dns.example.tld") {
		t.Error("Primary domain missing in lego arguments")
	}

	// Assertion: Verify wildcard domain presence.
	// This is critical for DoT/DoQ client differentiation via CNAME records.
	if !strings.Contains(args, "--domains *.dns.example.tld") {
		t.Error("Wildcard domain (*.dns.example.tld) missing for client tracking support")
	}

	// Token must be injected via env, not CLI args, to avoid /proc leakage.
	if !slices.Contains(cmd.Env, "CF_DNS_API_TOKEN=secret-token") {
		t.Error("CF_DNS_API_TOKEN was not found in the child process environment")
	}
}

func TestCertExists_Negative(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{ACMEDomain: "missing.invalid"}
	m := NewManager(cfg, nil)

	if m.certExists() {
		t.Error("certExists returned true for a non-existent certificate path")
	}
}
