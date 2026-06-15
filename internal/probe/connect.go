// Package probe implements the synthetic scenarios. Phase 1 ships the `connect`
// scenario; streaming / large_object / hifreq_small / scraping / long_session
// will land alongside it as additional functions following the same shape.
package probe

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

// ConnectScenario opens an HTTP CONNECT tunnel through proxy to target and
// measures dial_ms (client->proxy TCP) and connect_ms (CONNECT -> 200, the proxy
// establishing the upstream connection). If originGet is non-empty it then issues
// a plaintext HTTP GET over the tunnel to confirm end-to-end reachability and
// capture ttfb_ms — this only works against the self-hosted origin; for curated
// real sites on :443 leave originGet empty and the 200 to CONNECT is the signal.
func ConnectScenario(proxy *url.URL, target, originGet string, timeout time.Duration) model.ProbeResult {
	r := model.ProbeResult{
		Scenario: "connect",
		Proto:    "http",
		Target:   target,
		TS:       time.Now().UTC(),
	}

	start := time.Now()
	conn, err := (&net.Dialer{Timeout: timeout}).Dial("tcp", proxy.Host)
	if err != nil {
		r.ErrorType = classify("dial", err)
		r.TotalMS = ms(time.Since(start))
		return r
	}
	defer conn.Close()
	r.DialMS = ms(time.Since(start))
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// Send CONNECT and time the proxy's upstream establishment.
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if u := proxy.User; u != nil {
		pw, _ := u.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(u.Username() + ":" + pw))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", auth)
	}
	b.WriteString("\r\n")

	t1 := time.Now()
	if _, err := conn.Write([]byte(b.String())); err != nil {
		r.ErrorType = classify("connect_write", err)
		r.TotalMS = ms(time.Since(start))
		return r
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		r.ErrorType = classify("connect_read", err)
		r.TotalMS = ms(time.Since(start))
		return r
	}
	r.ConnectMS = ms(time.Since(t1))
	if resp.StatusCode != http.StatusOK {
		r.ErrorType = fmt.Sprintf("proxy_status_%d", resp.StatusCode)
		r.TotalMS = ms(time.Since(start))
		return r
	}

	if originGet != "" {
		t2 := time.Now()
		get := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", originGet, hostOnly(target))
		if _, err := conn.Write([]byte(get)); err != nil {
			r.ErrorType = classify("get_write", err)
			r.TotalMS = ms(time.Since(start))
			return r
		}
		gresp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
		if err != nil {
			r.ErrorType = classify("get_read", err)
			r.TotalMS = ms(time.Since(start))
			return r
		}
		r.TTFBMS = ms(time.Since(t2))
		n, _ := io.Copy(io.Discard, gresp.Body)
		gresp.Body.Close()
		r.Bytes = uint64(n)
		if gresp.StatusCode != http.StatusOK {
			r.ErrorType = fmt.Sprintf("origin_status_%d", gresp.StatusCode)
			r.TotalMS = ms(time.Since(start))
			return r
		}
	}

	r.Success = 1
	r.TotalMS = ms(time.Since(start))
	return r
}

func ms(d time.Duration) uint32 { return uint32(d.Milliseconds()) }

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func classify(stage string, err error) string {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return stage + "_timeout"
	case strings.Contains(s, "refused"):
		return stage + "_refused"
	case strings.Contains(s, "reset"):
		return stage + "_reset"
	case strings.Contains(s, "no route"), strings.Contains(s, "unreachable"):
		return stage + "_unreachable"
	default:
		return stage + "_error"
	}
}
