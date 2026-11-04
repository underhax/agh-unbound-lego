// Package health implements service readiness checks.
package health

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const checkTimeout = 2 * time.Second

type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

var dnsDialer contextDialer = &net.Dialer{Timeout: checkTimeout}
var httpClient = &http.Client{
	Timeout: checkTimeout,
	Transport: &http.Transport{
		DisableKeepAlives: true,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func defaultDialDNS(ctx context.Context, address string) (net.Conn, error) {
	conn, err := dnsDialer.DialContext(ctx, "udp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to dial DNS: %w", err)
	}
	return conn, nil
}

var dialDNS = defaultDialDNS

func defaultDoHTTP(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "http" || !strings.HasPrefix(req.URL.Host, "127.0.0.1:") {
		return nil, errors.New("HTTP healthcheck only permits requests to 127.0.0.1")
	}
	// #nosec G704 - Target URL is strictly restricted to local loopback above.
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}
	return resp, nil
}

var doHTTP = defaultDoHTTP

var randRead = rand.Read

// SetHooksForTest overrides network operations for testing purposes and returns a restore function.
func SetHooksForTest(dial func(context.Context, string) (net.Conn, error), do func(*http.Request) (*http.Response, error)) func() {
	origDial := dialDNS
	origDo := doHTTP
	if dial != nil {
		dialDNS = dial
	}
	if do != nil {
		doHTTP = do
	}
	return func() {
		dialDNS = origDial
		doHTTP = origDo
	}
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

	resp, doErr := doHTTP(req)
	if doErr != nil {
		return fmt.Errorf("AGH HTTP check failed (%s): %w", aghWebURL, doErr)
	}
	defer func() {
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

	parsedWeb, parsedDNS := parseEndpoints(yamlData)
	if parsedWeb != "" {
		webURL = parsedWeb
	}

	return webURL, parsedDNS, nil
}

func parseSectionField(section, key, val string) (parsedWeb, parsedDNS string) {
	if val == "" {
		return "", ""
	}
	switch section {
	case "http":
		if key == "address" {
			if _, port, splitErr := net.SplitHostPort(val); splitErr == nil {
				return fmt.Sprintf("http://127.0.0.1:%s/", port), ""
			}
		}
	case "dns":
		if key == "port" {
			if p, parseErr := strconv.Atoi(val); parseErr == nil && p > 0 {
				return "", fmt.Sprintf("127.0.0.1:%d", p)
			}
		}
	}
	return "", ""
}

func parseEndpoints(data []byte) (webURL, dnsPort string) {
	webURL = "http://127.0.0.1:3000/"
	var currentSection string

	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
			currentSection = strings.TrimSuffix(trimmed, ":")
			continue
		}

		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 {
			newWeb, newDNS := parseSectionField(currentSection, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
			if newWeb != "" {
				webURL = newWeb
			}
			if newDNS != "" {
				dnsPort = newDNS
			}
		}
	}

	return webURL, dnsPort
}

// CheckDNS sends a raw DNS query (Type A for root) to verify the resolver is functionally answering.
func CheckDNS(ctx context.Context, address string) error {
	conn, err := dialDNS(ctx, address)
	if err != nil {
		return fmt.Errorf("dialing %s failed: %w", address, err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			slog.Debug("failed to close UDP connection", "error", closeErr)
		}
	}()

	if err = conn.SetDeadline(time.Now().Add(checkTimeout)); err != nil {
		return fmt.Errorf("failed to set deadline: %w", err)
	}

	query := []byte("\xAA\xAA\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x03dns\x0bhealthcheck\x04arpa\x00\x00\x01\x00\x01")

	var txID [2]byte
	if _, err = randRead(txID[:]); err != nil {
		return fmt.Errorf("crypto/rand failed: %w", err)
	}
	query[0], query[1] = txID[0], txID[1]

	if _, err = conn.Write(query); err != nil {
		return fmt.Errorf("failed to send DNS query: %w", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("failed to read DNS response: %w", err)
	}

	if n < 12 {
		return fmt.Errorf("DNS response too short: %d bytes", n)
	}

	if buf[0] != txID[0] || buf[1] != txID[1] {
		return fmt.Errorf("DNS response transaction ID mismatch: got %02x %02x", buf[0], buf[1])
	}

	if buf[2]&0x80 == 0 {
		return errors.New("DNS response is not a reply (QR bit not set)")
	}

	rcode := buf[3] & 0x0F
	if rcode == 2 || rcode == 5 {
		return fmt.Errorf("DNS resolver returned error RCODE: %d", rcode)
	}

	return nil
}
