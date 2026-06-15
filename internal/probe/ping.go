package probe

import (
	"context"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

// Ping sends ICMP echo to the gateway host via the system `ping` and records the
// average RTT in connect_ms (scenario="ping"). This is the raw network latency to
// the gateway, independent of the proxy CONNECT path — useful to tell a network
// regression from a proxy regression.
func Ping(host string, count int, timeout time.Duration) model.ProbeResult {
	r := model.ProbeResult{Scenario: "ping", Proto: "icmp", Target: host, TS: time.Now().UTC()}
	if count <= 0 {
		count = 3
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(count), "-W", "2", "-n", host).CombinedOutput()
	avg := parsePingAvg(string(out))
	if avg < 0 {
		r.ErrorType = "ping_unreachable"
		return r
	}
	r.ConnectMS = uint32(math.Round(avg))
	r.Success = 1
	return r
}

// parsePingAvg pulls the average RTT (ms) from `ping` summary output, or -1.
// Linux: "rtt min/avg/max/mdev = 0.1/0.2/0.3/0.0 ms".
func parsePingAvg(s string) float64 {
	for _, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, "min/avg/max") {
			continue
		}
		i := strings.Index(line, "= ")
		if i < 0 {
			continue
		}
		fields := strings.Fields(line[i+2:]) // "0.1/0.2/0.3/0.0 ms"
		if len(fields) == 0 {
			continue
		}
		nums := strings.Split(fields[0], "/")
		if len(nums) >= 2 {
			if v, err := strconv.ParseFloat(nums[1], 64); err == nil {
				return v
			}
		}
	}
	return -1
}
