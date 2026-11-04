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

func defaultNewHTTPRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	return req, nil
}

var newHTTPRequest = defaultNewHTTPRequest

func defaultDoHTTP(ctx context.Context, port int) (*http.Response, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid HTTP healthcheck port")
	}
	req, err := newHTTPRequest(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create AGH HTTP request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}
	return resp, nil
}

var doHTTP = defaultDoHTTP

var randRead = rand.Read

// SetHooksForTest overrides network operations for testing purposes and returns a restore function.
func SetHooksForTest(dial func(context.Context, string) (net.Conn, error), do func(context.Context, int) (*http.Response, error)) func() {
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

	webPort, aghDNSPort, readErr := c.resolveEndpoints()
	if readErr != nil {
		return readErr
	}

	resp, doErr := doHTTP(context.Background(), webPort)
	if doErr != nil {
		return fmt.Errorf("AGH HTTP check failed (port %d): %w", webPort, doErr)
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

func (c *Checker) resolveEndpoints() (webPort int, dnsPort string, err error) {
	yamlData, readErr := os.ReadFile(c.AGHConfPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 3000, "", nil
		}
		return 0, "", fmt.Errorf("failed to read AGH config: %w", readErr)
	}

	parsedPort, parsedDNS := parseEndpoints(yamlData)
	return parsedPort, parsedDNS, nil
}

func extractPort(val string) int {
	cleaned := strings.Trim(val, " \"'")
	if _, portStr, err := net.SplitHostPort(cleaned); err == nil {
		if p, parseErr := strconv.Atoi(portStr); parseErr == nil && p > 0 && p <= 65535 {
			return p
		}
	}
	if p, parseErr := strconv.Atoi(cleaned); parseErr == nil && p > 0 && p <= 65535 {
		return p
	}
	return 0
}

func parseRootLine(trimmed string) string {
	if sec, ok := strings.CutSuffix(trimmed, ":"); ok {
		return sec
	}
	return ""
}

func parseSectionLine(section, trimmed string) (webPort int, dnsPort string) {
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) != 2 {
		return 0, ""
	}
	key := strings.TrimSpace(parts[0])
	val := strings.TrimSpace(parts[1])

	switch section {
	case "http":
		if key == "address" {
			return extractPort(val), ""
		}
	case "dns":
		if key == "port" {
			if p := extractPort(val); p > 0 {
				return 0, fmt.Sprintf("127.0.0.1:%d", p)
			}
		}
	}
	return 0, ""
}

func parseEndpoints(data []byte) (webPort int, dnsPort string) {
	var currentSection string
	var childIndent int

	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			currentSection = parseRootLine(trimmed)
			childIndent = 0
			continue
		}

		if currentSection == "" {
			continue
		}

		if childIndent == 0 {
			childIndent = indent
		}

		if indent != childIndent {
			continue
		}

		wp, dp := parseSectionLine(currentSection, trimmed)
		if wp > 0 && webPort == 0 {
			webPort = wp
		}
		if dp != "" && dnsPort == "" {
			dnsPort = dp
		}
	}

	if webPort == 0 {
		webPort = 80
	}

	return webPort, dnsPort
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
