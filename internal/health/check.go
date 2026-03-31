// Package health implements service readiness checks.
package health

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const checkTimeout = 2 * time.Second

// dnsDialer is used for UDP DNS queries, httpClient for TCP HTTP requests.
// This separation is intentional: UDP and TCP require different dialers, and
// connection pooling does not apply across protocol boundaries.
var dnsDialer = &net.Dialer{Timeout: checkTimeout}
var httpClient = &http.Client{
	Timeout: checkTimeout,
	Transport: &http.Transport{
		DisableKeepAlives: true,
	},
	// AGH admin panel should not redirect on the health probe path.
	// Following redirects risks consuming the timeout budget on TLS or auth endpoints.
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// aghConfig maps the minimal structure needed to extract binding ports.
type aghConfig struct {
	HTTP struct {
		Address string `yaml:"address"`
	} `yaml:"http"`
	DNS struct {
		Port int `yaml:"port"`
	} `yaml:"dns"`
}

// Checker provides external readiness probes decoupled from the internal process supervision tree.
type Checker struct {
	UnboundPort string
	AGHConfPath string
}

// Run executes the conditional healthcheck logic.
// It adapts to the AGH lifecycle: Setup Wizard (port 3000) vs Operational (parsed from YAML).
func (c *Checker) Run() (err error) {
	if chkErr := CheckDNS(context.Background(), c.UnboundPort); chkErr != nil {
		return fmt.Errorf("unbound DNS port check failed: %w", chkErr)
	}

	aghWebURL, aghDNSPort, readErr := c.resolveEndpoints()
	if readErr != nil {
		return readErr
	}

	req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet, aghWebURL, http.NoBody)
	if reqErr != nil {
		return fmt.Errorf("failed to create AGH HTTP request: %w", reqErr)
	}

	resp, doErr := httpClient.Do(req)
	if doErr != nil {
		return fmt.Errorf("AGH HTTP check failed (%s): %w", aghWebURL, doErr)
	}
	defer func() {
		// Drain the response body to allow connection reuse in the Keep-Alive pool
		if _, copyErr := io.Copy(io.Discard, resp.Body); copyErr != nil && err == nil {
			err = fmt.Errorf("failed to drain response body: %w", copyErr)
		}

		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close response body: %w", closeErr)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("AGH HTTP returned unexpected status: %d", resp.StatusCode)
	}

	if aghDNSPort != "" {
		if err := CheckDNS(context.Background(), aghDNSPort); err != nil {
			return fmt.Errorf("AGH DNS port check failed (%s): %w", aghDNSPort, err)
		}
	}

	return nil
}

// resolveEndpoints mitigates hardcoded assumptions by extracting dynamic port assignments negotiated during the setup wizard.
func (c *Checker) resolveEndpoints() (webURL, dnsPort string, err error) {
	webURL = "http://127.0.0.1:3000/"

	yamlData, readErr := os.ReadFile(c.AGHConfPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return webURL, "", nil
		}
		return "", "", fmt.Errorf("failed to read AGH config: %w", readErr)
	}

	var cfg aghConfig
	if parseErr := yaml.Unmarshal(yamlData, &cfg); parseErr == nil {
		if _, port, splitErr := net.SplitHostPort(cfg.HTTP.Address); splitErr == nil {
			webURL = fmt.Sprintf("http://127.0.0.1:%s/", port)
		}
		if cfg.DNS.Port > 0 {
			dnsPort = fmt.Sprintf("127.0.0.1:%d", cfg.DNS.Port)
		}
	}

	return webURL, dnsPort, nil
}

// CheckDNS sends a raw DNS query (Type A for root) to verify the resolver is functionally answering.
func CheckDNS(ctx context.Context, address string) error {
	conn, err := dnsDialer.DialContext(ctx, "udp", address)
	if err != nil {
		return fmt.Errorf("dialing %s failed: %w", address, err)
	}
	//nolint:errcheck // Socket is closed purely to release resources; cleanup errors are not actionable.
	defer conn.Close()

	// SetDeadline covers both Write and Read, bounding the entire exchange to 2 seconds.
	// SetReadDeadline alone would leave Write unprotected if the kernel UDP send buffer saturates.
	if err = conn.SetDeadline(time.Now().Add(checkTimeout)); err != nil {
		return fmt.Errorf("failed to set deadline: %w", err)
	}

	// RFC 9476 standardized QNAME 'dns.healthcheck.arpa' (Type A, Class IN).
	// Resolvers automatically exclude this domain from query logs to maintain clean statistics.
	query := []byte("\xAA\xAA\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x03dns\x0bhealthcheck\x04arpa\x00\x00\x01\x00\x01")

	// Randomize TxID to prevent spoofed responses on shared or compromised networks.
	var txID [2]byte
	rand.Read(txID[:]) // #nosec G104 -- crypto/rand uses OS CSPRNG, errors are unrecoverable
	query[0], query[1] = txID[0], txID[1]

	if _, err = conn.Write(query); err != nil {
		return fmt.Errorf("failed to send DNS query: %w", err)
	}

	// 4096 bytes accommodates EDNS0 and DNSSEC responses (DNSKEY + RRSIG can exceed 1200 bytes).
	// The pre-EDNS0 limit of 512 bytes causes silent truncation that still passes header validation.
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("failed to read DNS response: %w", err)
	}

	// Minimum valid DNS header per RFC 1035 §4.1.
	if n < 12 {
		return fmt.Errorf("DNS response too short: %d bytes", n)
	}

	if buf[0] != txID[0] || buf[1] != txID[1] {
		return fmt.Errorf("DNS response transaction ID mismatch: got %02x %02x", buf[0], buf[1])
	}

	if buf[2]&0x80 == 0 {
		return errors.New("DNS response is not a reply (QR bit not set)")
	}

	// Extract RCODE from the lower 4 bits of byte 3.
	// We consider SERVFAIL (2) and REFUSED (5) as definitive failures of the resolver's ability to serve.
	rcode := buf[3] & 0x0F
	if rcode == 2 || rcode == 5 {
		return fmt.Errorf("DNS resolver returned error RCODE: %d", rcode)
	}

	return nil
}
