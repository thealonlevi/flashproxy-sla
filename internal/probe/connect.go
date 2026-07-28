// Package probe implements the synthetic scenarios: connect (the SLA headline),
// streaming / large_object / hifreq_small / scraping / long_session, net_rtt (a
// stdlib TCP-connect RTT to the gateway, not ICMP), and a direct (no-proxy) baseline
// variant of most (suffix "_direct"; net_rtt has no _direct form).
package probe

import (
	"math"
	"math/rand"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

// Endpoint is one connect-scenario target: a host:port the proxy CONNECTs to, plus a
// plaintext GET path used to measure ttfb (empty => the established tunnel is the
// signal, no GET). Hostnames let the proxy resolve the right address family, so the
// same anycast endpoints work for v4 and v6-egress packages.
type Endpoint struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	// Group is the provider family (e.g. "google", "mozilla", "cloudflare").
	// Stratified sampling probes one endpoint from each of several groups per
	// cycle, so the pool stays provider-diverse without probing all N every
	// cycle. Group "origin" (or empty) is ALWAYS probed — it is our own
	// availability floor and guarantees a cycle can never draw an all-blocked
	// sample of third parties.
	Group string `json:"group"`
}

// GroupsPerCycle is how many non-origin provider groups are sampled each cycle
// (one endpoint from each). Bounds third-party load and keeps the sample
// provider-diverse. Origin endpoints are always probed on top of this.
const GroupsPerCycle = 5

// stratifiedSample selects the endpoints to probe this cycle: every "origin"
// (or ungrouped) endpoint, plus one randomly-chosen endpoint from each of up to
// groupsPerCycle other provider groups (the groups themselves chosen at random).
// So per-cycle third-party load is bounded and diverse, and the origin floor is
// always present. Pure and deterministic given the RNG the global rand provides.
func stratifiedSample(eps []Endpoint, groupsPerCycle int) []Endpoint {
	if groupsPerCycle <= 0 {
		groupsPerCycle = GroupsPerCycle
	}
	var always []Endpoint
	groups := map[string][]Endpoint{}
	var order []string // group names in first-seen order (stable input to the shuffle)
	for _, e := range eps {
		if e.Group == "" || e.Group == "origin" {
			always = append(always, e)
			continue
		}
		if _, ok := groups[e.Group]; !ok {
			order = append(order, e.Group)
		}
		groups[e.Group] = append(groups[e.Group], e)
	}
	rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	if len(order) > groupsPerCycle {
		order = order[:groupsPerCycle]
	}
	out := append([]Endpoint(nil), always...)
	for _, g := range order {
		m := groups[g]
		out = append(out, m[rand.Intn(len(m))])
	}
	return out
}

// ConnectBest probes a stratified sample of the endpoint pool concurrently and
// returns (1) the single BEST result — among endpoints that succeeded, the
// lowest ttfb wins; reachable if ANY succeeded, Down only if every sampled
// endpoint failed — which drives the SLO verdict, and (2) the per-target
// results relabeled to the "connect_probe" scenario, so a target going bad
// (e.g. a provider blocking proxy egress) is visible even though only the winner
// counts. The best row carries the winning target; downstream (rollup / ledger /
// incidents) is unchanged.
func ConnectBest(proxy *url.URL, eps []Endpoint, timeout time.Duration) (best model.ProbeResult, probes []model.ProbeResult) {
	if len(eps) == 0 {
		return model.ProbeResult{Scenario: scn("connect", proxy), Proto: "http", TS: time.Now().UTC(), ErrorType: "no_targets"}, nil
	}
	sample := stratifiedSample(eps, GroupsPerCycle)

	results := make([]model.ProbeResult, len(sample))
	var wg sync.WaitGroup
	for i, ep := range sample {
		wg.Add(1)
		go func(i int, ep Endpoint) {
			defer wg.Done()
			results[i] = ConnectScenario(proxy, ep.Target, ep.Path, timeout)
		}(i, ep)
	}
	wg.Wait()

	best = results[0]
	for _, r := range results[1:] {
		best = betterConnect(best, r)
	}

	// Per-target diagnostic rows under a separate scenario. The SLO reads only
	// scenario='connect', so these change no verdict — they make per-target
	// health (which ConnectBest otherwise hides behind the winner) visible.
	probeScn := scn("connect_probe", proxy)
	probes = make([]model.ProbeResult, len(results))
	for i, r := range results {
		r.Scenario = probeScn
		probes[i] = r
	}
	return best, probes
}

// betterConnect prefers a success over a failure; between two successes the lower
// ttfb (tie-break lower connect_ms) wins; between two failures it keeps the first
// (the row is Down regardless).
func betterConnect(a, b model.ProbeResult) model.ProbeResult {
	if a.Success != b.Success {
		if a.Success == 1 {
			return a
		}
		return b
	}
	if a.Success == 1 && (b.TTFBMS < a.TTFBMS || (b.TTFBMS == a.TTFBMS && b.ConnectMS < a.ConnectMS)) {
		return b
	}
	return a
}

// ConnectScenario measures dial_ms and connect_ms to target. With a proxy it's an
// HTTP CONNECT tunnel (connect_ms = proxy upstream establishment); with proxy==nil
// it's a direct TCP connect from the vantage (the no-proxy baseline, "connect_direct").
// If originGet is non-empty it issues a plaintext GET over the connection to confirm
// reachability and capture ttfb_ms (use against the self-hosted origin; for real
// :443 sites leave it empty and the established connection is the signal).
func ConnectScenario(proxy *url.URL, target, originGet string, timeout time.Duration) model.ProbeResult {
	r := model.ProbeResult{Scenario: scn("connect", proxy), Proto: "http", Target: target, TS: time.Now().UTC()}
	start := time.Now()
	conn, br, dialMs, connMs, et := openTunnel(proxy, target, timeout)
	r.DialMS, r.ConnectMS = dialMs, connMs
	if et != "" {
		r.ErrorType = et
		r.TotalMS = ms(time.Since(start))
		return r
	}
	defer conn.Close()

	if originGet != "" {
		ttfb, n, _, status, et2 := getOverTunnel(conn, br, hostOnly(target), originGet, timeout)
		r.TTFBMS, r.Bytes = ttfb, uint64(n)
		if et2 != "" {
			r.ErrorType = et2
			r.TotalMS = ms(time.Since(start))
			return r
		}
		// Accept any 2xx. Connectivity-check endpoints answer 200 (with a tiny
		// body) or 204 (No Content) — both are "reachable", and both record ttfb
		// at the headers, so the latency metric stays meaningful.
		if status < 200 || status >= 300 {
			r.ErrorType = "origin_status_" + strconv.Itoa(status)
			r.TotalMS = ms(time.Since(start))
			return r
		}
	}
	r.Success = 1
	r.TotalMS = ms(time.Since(start))
	return r
}

// ms converts a duration to milliseconds, ROUNDING to nearest (not truncating) so a
// 1.9 ms operation records 2, not 1. Sub-half-millisecond ops still round to 0,
// which is fine at the SLA's tens-of-ms granularity.
func ms(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	return uint32(math.Round(float64(d) / float64(time.Millisecond)))
}

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
