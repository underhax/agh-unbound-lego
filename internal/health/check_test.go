package health

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mockDNSConn struct {
	readErr     error
	writeErr    error
	closeErr    error
	deadlineErr error
	response    []byte
	skipEchoID  bool
}

func (m *mockDNSConn) Read(b []byte) (int, error) {
	if m.readErr != nil {
		return 0, m.readErr
	}
	return copy(b, m.response), nil
}

func (m *mockDNSConn) Write(b []byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	if !m.skipEchoID && len(b) >= 2 && len(m.response) >= 2 {
		m.response[0] = b[0]
		m.response[1] = b[1]
	}
	return len(b), nil
}

func (m *mockDNSConn) Close() error                       { return m.closeErr }
func (m *mockDNSConn) LocalAddr() net.Addr                { return &net.IPAddr{} }
func (m *mockDNSConn) RemoteAddr() net.Addr               { return &net.IPAddr{} }
func (m *mockDNSConn) SetDeadline(_ time.Time) error      { return m.deadlineErr }
func (m *mockDNSConn) SetReadDeadline(_ time.Time) error  { return nil }
func (m *mockDNSConn) SetWriteDeadline(_ time.Time) error { return nil }

type mockBody struct {
	readErr  error
	closeErr error
}

func (m *mockBody) Read(_ []byte) (int, error) {
	if m.readErr != nil {
		return 0, m.readErr
	}
	return 0, io.EOF
}

func (m *mockBody) Close() error {
	return m.closeErr
}

func validDNSHeader() []byte {
	return []byte{0x00, 0x00, 0x80, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00}
}

func TestCheckDNS(t *testing.T) {
	origDial := dialDNS
	t.Cleanup(func() { dialDNS = origDial })

	tests := []struct {
		dialFn  func(context.Context, string) (net.Conn, error)
		name    string
		wantErr bool
	}{
		{
			name: "Success",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			wantErr: false,
		},
		{
			name: "DialFailure",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, errors.New("dial error")
			},
			wantErr: true,
		},
		{
			name: "WriteFailure",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{writeErr: errors.New("write error")}, nil
			},
			wantErr: true,
		},
		{
			name: "ReadFailure",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{readErr: errors.New("read error")}, nil
			},
			wantErr: true,
		},
		{
			name: "ResponseTooShort",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: []byte{0x00, 0x01, 0x02}}, nil
			},
			wantErr: true,
		},
		{
			name: "TxIDMismatch",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{
					response:   []byte{0xFF, 0xFF, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
					skipEchoID: true,
				}, nil
			},
			wantErr: true,
		},
		{
			name: "NotReplyQRZero",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				resp := validDNSHeader()
				resp[2] = 0x00
				return &mockDNSConn{response: resp}, nil
			},
			wantErr: true,
		},
		{
			name: "ErrorRCODE_SERVFAIL",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				resp := validDNSHeader()
				resp[3] = 0x02
				return &mockDNSConn{response: resp}, nil
			},
			wantErr: true,
		},
		{
			name: "ErrorRCODE_REFUSED",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				resp := validDNSHeader()
				resp[3] = 0x05
				return &mockDNSConn{response: resp}, nil
			},
			wantErr: true,
		},
		{
			name: "CloseFailure",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{
					response: validDNSHeader(),
					closeErr: errors.New("close error"),
				}, nil
			},
			wantErr: false,
		},
		{
			name: "SetDeadlineFailure",
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{
					deadlineErr: errors.New("deadline error"),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dialDNS = tc.dialFn
			err := CheckDNS(context.Background(), "127.0.0.1:5053")
			if (err != nil) != tc.wantErr {
				t.Errorf("CheckDNS() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestCheckDNS_RandFailure(t *testing.T) {
	origRand := randRead
	randRead = func(_ []byte) (int, error) {
		return 0, errors.New("rand error")
	}
	t.Cleanup(func() { randRead = origRand })

	origDial := dialDNS
	dialDNS = func(_ context.Context, _ string) (net.Conn, error) {
		return &mockDNSConn{response: validDNSHeader()}, nil
	}
	t.Cleanup(func() { dialDNS = origDial })

	if err := CheckDNS(context.Background(), "127.0.0.1:5053"); err == nil {
		t.Error("expected error when randRead fails")
	}
}

func TestChecker_Run(t *testing.T) {
	origDial := dialDNS
	origHTTP := doHTTP
	t.Cleanup(func() {
		dialDNS = origDial
		doHTTP = origHTTP
	})

	tempDir := t.TempDir()
	confPath := filepath.Join(tempDir, "AdGuardHome.yaml")
	if err := os.WriteFile(confPath, []byte("http:\n  address: 0.0.0.0:80\ndns:\n  port: 53\n"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	invalidURLConf := filepath.Join(tempDir, "invalid_url.yaml")
	if err := os.WriteFile(invalidURLConf, []byte("http:\n  address: 0.0.0.0:80\x7f\n"), 0o600); err != nil {
		t.Fatalf("failed to write invalid url config: %v", err)
	}

	tests := []struct {
		dialFn   func(context.Context, string) (net.Conn, error)
		httpFn   func(*http.Request) (*http.Response, error)
		name     string
		confFile string
		wantErr  bool
	}{
		{
			name:     "OperationalMode_ValidConfig",
			confFile: confPath,
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("OK")),
				}, nil
			},
			wantErr: false,
		},
		{
			name:     "SetupWizardMode_NoConfigFile",
			confFile: filepath.Join(tempDir, "nonexistent.yaml"),
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("OK")),
				}, nil
			},
			wantErr: false,
		},
		{
			name:     "UnboundDown_Failure",
			confFile: confPath,
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return nil, errors.New("unbound unreachable")
			},
			httpFn:  origHTTP,
			wantErr: true,
		},
		{
			name:     "AGH_HTTP_NetworkFailure",
			confFile: confPath,
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("connection refused")
			},
			wantErr: true,
		},
		{
			name:     "AGH_HTTP_UnexpectedStatus",
			confFile: confPath,
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("Internal Error")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:     "AGH_DNS_PortFailure",
			confFile: confPath,
			dialFn: func() func(context.Context, string) (net.Conn, error) {
				callCount := 0
				return func(_ context.Context, _ string) (net.Conn, error) {
					callCount++
					if callCount == 1 {
						return &mockDNSConn{response: validDNSHeader()}, nil
					}
					return nil, errors.New("agh dns port unreachable")
				}
			}(),
			httpFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("OK")),
				}, nil
			},
			wantErr: true,
		},
		{
			name:     "ConfigReadError",
			confFile: t.TempDir(),
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn:  origHTTP,
			wantErr: true,
		},
		{
			name:     "InvalidURL_Failure",
			confFile: invalidURLConf,
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn:  origHTTP,
			wantErr: true,
		},
		{
			name:     "BodyDrainError",
			confFile: confPath,
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       &mockBody{readErr: errors.New("drain error")},
				}, nil
			},
			wantErr: true,
		},
		{
			name:     "BodyCloseError",
			confFile: confPath,
			dialFn: func(_ context.Context, _ string) (net.Conn, error) {
				return &mockDNSConn{response: validDNSHeader()}, nil
			},
			httpFn: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       &mockBody{closeErr: errors.New("close error")},
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dialDNS = tc.dialFn
			doHTTP = tc.httpFn

			checker := &Checker{
				UnboundPort: "127.0.0.1:5053",
				AGHConfPath: tc.confFile,
			}
			err := checker.Run()
			if (err != nil) != tc.wantErr {
				t.Errorf("checker.Run() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

type mockContextDialer struct {
	dialFn func(ctx context.Context, network, address string) (net.Conn, error)
}

func (m *mockContextDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return m.dialFn(ctx, network, address)
}

func TestDefaultDialDNS(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := defaultDialDNS(ctx, "127.0.0.1:53"); err == nil {
		t.Error("expected error on cancelled context dial")
	}

	origDialer := dnsDialer
	dnsDialer = &mockContextDialer{
		dialFn: func(_ context.Context, _, _ string) (net.Conn, error) {
			return &mockDNSConn{}, nil
		},
	}
	t.Cleanup(func() { dnsDialer = origDialer })

	conn, err := defaultDialDNS(t.Context(), "127.0.0.1:53")
	if err != nil {
		t.Errorf("expected success from defaultDialDNS, got %v", err)
	} else if conn == nil {
		t.Error("expected non-nil conn")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDefaultDoHTTP(t *testing.T) {
	reqBad, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", http.NoBody)
	if err != nil {
		t.Fatalf("failed to create bad request: %v", err)
	}
	respBad, err := defaultDoHTTP(reqBad)
	if err == nil {
		if closeErr := respBad.Body.Close(); closeErr != nil {
			t.Logf("close error: %v", closeErr)
		}
		t.Error("expected error for non-loopback request")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	reqCancel, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:3000", http.NoBody)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	respCancel, err := defaultDoHTTP(reqCancel)
	if err == nil {
		defer func() {
			if closeErr := respCancel.Body.Close(); closeErr != nil {
				t.Logf("close error: %v", closeErr)
			}
		}()
		t.Error("expected error on cancelled context http")
	}

	origTransport := httpClient.Transport
	t.Cleanup(func() { httpClient.Transport = origTransport })

	httpClient.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/redirect" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": {"/target"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("OK")),
			Request:    req,
		}, nil
	})

	reqNormal, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:3000/ok", http.NoBody)
	if err != nil {
		t.Fatalf("failed to create normal request: %v", err)
	}
	respNormal, err := defaultDoHTTP(reqNormal)
	if err != nil {
		t.Errorf("expected success from defaultDoHTTP, got %v", err)
	} else {
		if closeErr := respNormal.Body.Close(); closeErr != nil {
			t.Logf("failed to close respNormal: %v", closeErr)
		}
	}

	reqRedirect, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:3000/redirect", http.NoBody)
	if err != nil {
		t.Fatalf("failed to create redirect request: %v", err)
	}
	respRedirect, err := defaultDoHTTP(reqRedirect)
	if err != nil {
		t.Errorf("expected success with ErrUseLastResponse on redirect, got %v", err)
	} else {
		if closeErr := respRedirect.Body.Close(); closeErr != nil {
			t.Logf("failed to close respRedirect: %v", closeErr)
		}
	}
}

func TestSetHooksForTest(t *testing.T) {
	dialCalled := false
	httpCalled := false
	restore := SetHooksForTest(
		func(_ context.Context, _ string) (net.Conn, error) {
			dialCalled = true
			return nil, errors.New("dial hook")
		},
		func(_ *http.Request) (*http.Response, error) {
			httpCalled = true
			return nil, errors.New("http hook")
		},
	)

	if _, err := dialDNS(context.Background(), "127.0.0.1:53"); err == nil {
		t.Error("expected error from dial hook")
	}
	if !dialCalled {
		t.Error("expected dial hook to be called")
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1", http.NoBody)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	resp, err := doHTTP(req)
	if err == nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Logf("close error: %v", closeErr)
		}
		t.Error("expected error from http hook")
	}
	if !httpCalled {
		t.Error("expected http hook to be called")
	}

	restore()
}

func TestParseEndpoints_EdgeCases(t *testing.T) {
	w, d := parseSectionField("http", "address", "")
	if w != "" || d != "" {
		t.Errorf("expected empty results for empty val, got web=%q dns=%q", w, d)
	}

	data := "http:\n  address: \n  address: notaport\ndns:\n  port: notanint\n  port: -5\nunknown:\n  key: val\n"
	web, dns := parseEndpoints([]byte(data))
	if web != "http://127.0.0.1:3000/" || dns != "" {
		t.Errorf("unexpected parse result: web=%s dns=%s", web, dns)
	}
}
