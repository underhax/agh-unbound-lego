package health

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func startMockDNS(t *testing.T) (addr string, cleanup func()) {
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("Failed to start mock DNS: %v", err)
	}
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := ln.ReadFromUDP(buf)
			if err != nil {
				return
			}
			// Set the QR bit (byte 2, highest bit) to indicate it's a response
			if n >= 3 {
				buf[2] |= 0x80
			}
			//nolint:errcheck,gosec // Mock server response is best-effort.
			ln.WriteToUDP(buf[:n], addr)
		}
	}()
	//nolint:errcheck,gosec // Test teardown cleanup is best-effort.
	return ln.LocalAddr().String(), func() { ln.Close() }
}

func TestChecker_Run(t *testing.T) {
	t.Parallel()

	unboundPort, closeUnbound := startMockDNS(t)
	defer closeUnbound()

	aghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer aghSrv.Close()

	_, aghHTTPPort, err := net.SplitHostPort(aghSrv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("Failed to split AGH HTTP host/port: %v", err)
	}

	dnsAddr, closeDNS := startMockDNS(t)
	defer closeDNS()
	_, aghDNSPortStr, err := net.SplitHostPort(dnsAddr)
	if err != nil {
		t.Fatalf("Failed to split AGH DNS host/port: %v", err)
	}

	tempDir := t.TempDir()
	confPath := filepath.Join(tempDir, "AdGuardHome.yaml")

	t.Run("OperationalMode_ValidConfig", func(t *testing.T) {
		yamlContent := fmt.Sprintf("http:\n  address: 0.0.0.0:%s\ndns:\n  port: %s\n", aghHTTPPort, aghDNSPortStr)
		if err := os.WriteFile(confPath, []byte(yamlContent), 0o600); err != nil {
			t.Fatalf("Failed to write mock config: %v", err)
		}

		checker := &Checker{
			UnboundPort: unboundPort,
			AGHConfPath: confPath,
		}

		if err := checker.Run(); err != nil {
			t.Errorf("Expected healthcheck to pass, got: %v", err)
		}
	})

	t.Run("UnboundDown_Failure", func(t *testing.T) {
		checker := &Checker{
			UnboundPort: "127.0.0.1:1",
			AGHConfPath: confPath,
		}

		if err := checker.Run(); err == nil {
			t.Error("Expected failure when Unbound is down, got success")
		}
	})

	t.Run("AGH_HTTP_Failure", func(t *testing.T) {
		yamlContent := "http:\n  address: 0.0.0.0:1\ndns:\n  port: 53\n"
		if err := os.WriteFile(confPath, []byte(yamlContent), 0o600); err != nil {
			t.Fatalf("Failed to write mock config: %v", err)
		}

		checker := &Checker{
			UnboundPort: unboundPort,
			AGHConfPath: confPath,
		}

		if err := checker.Run(); err == nil {
			t.Error("Expected failure when AGH HTTP is down, got success")
		}
	})
}
