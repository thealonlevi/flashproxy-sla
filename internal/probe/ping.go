package probe

import (
	"net"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

// NetRTT measures raw network round-trip to the proxy gateway by timing TCP
// connects (scenario="net_rtt"), recording the average in connect_ms. This is the
// network floor independent of the proxy's CONNECT path, so a network regression
// can be told apart from a proxy regression.
//
// It replaces the previous ICMP shell-out (`ping`), which silently failed in the
// distroless runtime image (no ping binary) and required raw-socket privileges.
// TCP-connect RTT is stdlib-only, runs unprivileged, and works in any container.
// hostport must be host:port (e.g. the proxy's own host:port).
func NetRTT(hostport string, count int, timeout time.Duration) model.ProbeResult {
	r := model.ProbeResult{Scenario: "net_rtt", Proto: "tcp", Target: hostport, TS: time.Now().UTC()}
	if count <= 0 {
		count = 3
	}
	var sum time.Duration
	var ok int
	for i := 0; i < count; i++ {
		start := time.Now()
		conn, err := (&net.Dialer{Timeout: timeout}).Dial("tcp", hostport)
		if err != nil {
			r.ErrorType = classify("net_rtt", err)
			continue
		}
		sum += time.Since(start)
		ok++
		conn.Close()
	}
	if ok == 0 {
		// keep the classified error from the last attempt; success stays 0
		return r
	}
	r.ConnectMS = ms(sum / time.Duration(ok))
	r.ErrorType = ""
	r.Success = 1
	return r
}
