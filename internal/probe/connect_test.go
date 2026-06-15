package probe

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// tinyConnectProxy is a minimal HTTP CONNECT proxy that requires Basic
// Proxy-Authorization, used to exercise ConnectScenario end-to-end.
func tinyConnectProxy(t *testing.T, wantAuth string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConnect(c, wantAuth)
		}
	}()
	return ln.Addr().String()
}

func serveConnect(c net.Conn, wantAuth string) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		io.WriteString(c, "HTTP/1.1 400 Bad Request\r\n\r\n")
		return
	}
	if got := req.Header.Get("Proxy-Authorization"); got != wantAuth {
		io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
		return
	}
	up, err := net.Dial("tcp", req.Host)
	if err != nil {
		io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	// Pump any buffered client bytes first, then splice both directions.
	go func() {
		if n := br.Buffered(); n > 0 {
			b, _ := br.Peek(n)
			up.Write(b)
			br.Discard(n)
		}
		io.Copy(up, br)
	}()
	io.Copy(c, up)
}

func TestConnectScenarioSuccess(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/connect" {
			io.WriteString(w, "ok")
			return
		}
		http.NotFound(w, r)
	}))
	defer origin.Close()
	target := strings.TrimPrefix(origin.URL, "http://") // host:port

	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	proxyAddr := tinyConnectProxy(t, auth)
	proxy, _ := url.Parse("http://user:pass@" + proxyAddr)

	r := ConnectScenario(proxy, target, "/connect", 5*time.Second)
	if r.Success != 1 {
		t.Fatalf("expected success, got err=%q", r.ErrorType)
	}
	if r.ErrorType != "" {
		t.Fatalf("unexpected error type: %q", r.ErrorType)
	}
	if r.Bytes == 0 {
		t.Errorf("expected body bytes from origin GET, got 0")
	}
	if r.Scenario != "connect" || r.Proto != "http" {
		t.Errorf("unexpected labels: %+v", r)
	}
}

func TestConnectScenarioBadAuth(t *testing.T) {
	proxyAddr := tinyConnectProxy(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("right:creds")))
	proxy, _ := url.Parse("http://wrong:creds@" + proxyAddr)

	r := ConnectScenario(proxy, "example.com:80", "", 5*time.Second)
	if r.Success == 1 {
		t.Fatal("expected failure on bad proxy auth")
	}
	if r.ErrorType != "proxy_status_407" {
		t.Errorf("expected proxy_status_407, got %q", r.ErrorType)
	}
}

func TestConnectScenarioDialRefused(t *testing.T) {
	// Port 1 on loopback should refuse quickly.
	proxy, _ := url.Parse("http://127.0.0.1:1")
	r := ConnectScenario(proxy, "example.com:80", "", 2*time.Second)
	if r.Success == 1 {
		t.Fatal("expected dial failure")
	}
	if !strings.HasPrefix(r.ErrorType, "dial_") {
		t.Errorf("expected dial_* error, got %q", r.ErrorType)
	}
}
