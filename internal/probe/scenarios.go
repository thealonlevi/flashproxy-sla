package probe

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

// openTunnel opens an HTTP CONNECT tunnel through the proxy to target and returns
// the live connection + buffered reader, plus dial_ms (client->proxy) and
// connect_ms (proxy upstream establishment). On error, conn is nil.
func openTunnel(proxy *url.URL, target string, timeout time.Duration) (net.Conn, *bufio.Reader, uint32, uint32, string) {
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: timeout}).Dial("tcp", proxy.Host)
	if err != nil {
		return nil, nil, 0, 0, classify("dial", err)
	}
	dialMs := ms(time.Since(start))
	_ = conn.SetDeadline(time.Now().Add(timeout))

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
		conn.Close()
		return nil, nil, dialMs, 0, classify("connect_write", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		conn.Close()
		return nil, nil, dialMs, 0, classify("connect_read", err)
	}
	connectMs := ms(time.Since(t1))
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, nil, dialMs, connectMs, fmt.Sprintf("proxy_status_%d", resp.StatusCode)
	}
	return conn, br, dialMs, connectMs, ""
}

// getOverTunnel issues a plaintext GET over an established tunnel and drains the
// body, returning ttfb_ms, bytes, throughput, and status.
func getOverTunnel(conn net.Conn, br *bufio.Reader, host, path string, timeout time.Duration) (ttfb uint32, n int64, mbps float32, status int, errType string) {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nConnection: close\r\n\r\n"
	start := time.Now()
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, 0, 0, 0, classify("get_write", err)
	}
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		return 0, 0, 0, 0, classify("get_read", err)
	}
	ttfb = ms(time.Since(start))
	n, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if d := time.Since(start).Seconds(); d > 0 {
		mbps = float32(float64(n) * 8 / d / 1e6)
	}
	return ttfb, n, mbps, resp.StatusCode, ""
}

// Streaming mimics heavy streaming/buffering: pull a large object and measure
// sustained throughput + TTFB.
func Streaming(proxy *url.URL, origin string, sizeBytes int, timeout time.Duration) model.ProbeResult {
	return download("streaming", proxy, origin, sizeBytes, timeout)
}

// LargeObject mimics the large-object archetype: a medium object; connect/TTFB matter.
func LargeObject(proxy *url.URL, origin string, sizeBytes int, timeout time.Duration) model.ProbeResult {
	return download("large_object", proxy, origin, sizeBytes, timeout)
}

func download(scenario string, proxy *url.URL, origin string, sizeBytes int, timeout time.Duration) model.ProbeResult {
	r := model.ProbeResult{Scenario: scenario, Proto: "http", Target: origin, TS: time.Now().UTC()}
	start := time.Now()
	conn, br, dialMs, connMs, et := openTunnel(proxy, origin, timeout)
	r.DialMS, r.ConnectMS = dialMs, connMs
	if et != "" {
		r.ErrorType = et
		r.TotalMS = ms(time.Since(start))
		return r
	}
	defer conn.Close()
	ttfb, n, mbps, status, et2 := getOverTunnel(conn, br, hostOnly(origin), fmt.Sprintf("/bytes/%d", sizeBytes), timeout)
	r.TTFBMS, r.Bytes, r.ThroughputMbps = ttfb, uint64(n), mbps
	r.TotalMS = ms(time.Since(start))
	if et2 != "" {
		r.ErrorType = et2
		return r
	}
	if status != http.StatusOK {
		r.ErrorType = fmt.Sprintf("origin_status_%d", status)
		return r
	}
	if int(n) != sizeBytes {
		r.ErrorType = "short_read"
		return r
	}
	r.Success = 1
	return r
}

// HifreqSmall mimics account/credential checkers: k fresh connections each pulling
// a tiny body. Emits one row per attempt so the connect-ms distribution and setup
// success rate aggregate naturally.
func HifreqSmall(proxy *url.URL, origin string, k int, timeout time.Duration) []model.ProbeResult {
	out := make([]model.ProbeResult, 0, k)
	for i := 0; i < k; i++ {
		r := model.ProbeResult{Scenario: "hifreq_small", Proto: "http", Target: origin, TS: time.Now().UTC()}
		start := time.Now()
		conn, br, dialMs, connMs, et := openTunnel(proxy, origin, timeout)
		r.DialMS, r.ConnectMS = dialMs, connMs
		if et != "" {
			r.ErrorType = et
			r.TotalMS = ms(time.Since(start))
			out = append(out, r)
			continue
		}
		ttfb, n, _, status, et2 := getOverTunnel(conn, br, hostOnly(origin), "/small", timeout)
		conn.Close()
		r.TTFBMS, r.Bytes, r.TotalMS = ttfb, uint64(n), ms(time.Since(start))
		if et2 != "" {
			r.ErrorType = et2
		} else if status != http.StatusOK {
			r.ErrorType = fmt.Sprintf("origin_status_%d", status)
		} else {
			r.Success = 1
		}
		out = append(out, r)
	}
	return out
}

// Scraping mimics broad web scraping: CONNECT to many distinct real hosts and
// measure the connect-ms spread. One row per host.
func Scraping(proxy *url.URL, hosts []string, timeout time.Duration) []model.ProbeResult {
	out := make([]model.ProbeResult, 0, len(hosts))
	for _, h := range hosts {
		r := model.ProbeResult{Scenario: "scraping", Proto: "http", Target: h, TS: time.Now().UTC()}
		start := time.Now()
		conn, _, dialMs, connMs, et := openTunnel(proxy, h, timeout)
		r.DialMS, r.ConnectMS = dialMs, connMs
		r.TotalMS = ms(time.Since(start))
		if conn != nil {
			conn.Close()
		}
		if et != "" {
			r.ErrorType = et
		} else {
			r.Success = 1
		}
		out = append(out, r)
	}
	return out
}

// LongSession mimics long-maintained/persistent sessions: hold a tunnel open for
// holdMs while the origin trickles bytes, and record how long it stayed up.
func LongSession(proxy *url.URL, origin string, holdMs int, timeout time.Duration) model.ProbeResult {
	r := model.ProbeResult{Scenario: "long_session", Proto: "http", Target: origin, TS: time.Now().UTC()}
	start := time.Now()
	conn, br, dialMs, connMs, et := openTunnel(proxy, origin, timeout)
	r.DialMS, r.ConnectMS = dialMs, connMs
	if et != "" {
		r.ErrorType = et
		r.TotalMS = ms(time.Since(start))
		return r
	}
	defer conn.Close()
	hold := time.Duration(holdMs) * time.Millisecond
	_ = conn.SetDeadline(time.Now().Add(hold + 5*time.Second))
	req := "GET /hold?ms=" + strconv.Itoa(holdMs) + " HTTP/1.1\r\nHost: " + hostOnly(origin) + "\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		r.ErrorType = classify("get_write", err)
		return r
	}
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		r.ErrorType = classify("get_read", err)
		return r
	}
	r.TTFBMS = ms(time.Since(start))
	buf := make([]byte, 256)
	var n int64
	for {
		k, e := resp.Body.Read(buf)
		n += int64(k)
		if e != nil {
			break
		}
	}
	resp.Body.Close()
	held := time.Since(start)
	r.Bytes, r.TotalMS = uint64(n), ms(held)
	if held >= hold*9/10 {
		r.Success = 1
	} else {
		r.ErrorType = "session_dropped_early"
	}
	return r
}
