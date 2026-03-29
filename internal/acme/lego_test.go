package acme

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/agh-unbound-lego/internal/config"
)

func TestBuildCmd_WildcardAndEnv(t *testing.T) {
	// Setup: Mock configuration with Lego enabled and explicit domain
	cfg := &config.Config{
		ACMEDomain: "dns.example.tld",
		ACMEEmail:  "admin@test.local",
		CFDNSToken: "secret-token",
		LegoEnable: true,
	}
	m := NewManager(cfg, nil)

	// Action: Build the execution command for initial certificate run
	cmd := m.buildCmd(context.Background(), "run")
	args := strings.Join(cmd.Args, " ")

	// Assertion: Verify main domain presence
	if !strings.Contains(args, "--domains dns.example.tld") {
		t.Error("Primary domain missing in lego arguments")
	}

	// Assertion: Verify wildcard domain presence.
	// This is critical for DoT/DoQ client differentiation via CNAME records.
	if !strings.Contains(args, "--domains *.dns.example.tld") {
		t.Error("Wildcard domain (*.dns.example.tld) missing for client tracking support")
	}

	// Assertion: Ensure the Cloudflare token is securely propagated via Environment Variables
	// rather than command-line arguments to prevent leakage in process lists.
	if !slices.Contains(cmd.Env, "CF_DNS_API_TOKEN=secret-token") {
		t.Error("CF_DNS_API_TOKEN was not found in the child process environment")
	}
}

func TestCertExists_Negative(t *testing.T) {
	// Assertion: Ensure the check fails gracefully when no certificates are present.
	cfg := &config.Config{ACMEDomain: "missing.invalid"}
	m := NewManager(cfg, nil)

	if m.certExists() {
		t.Error("certExists returned true for a non-existent certificate path")
	}
}
