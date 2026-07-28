package probe

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

func TestBetterConnect(t *testing.T) {
	ok := func(ttfb, conn uint32) model.ProbeResult {
		return model.ProbeResult{Success: 1, TTFBMS: ttfb, ConnectMS: conn}
	}
	fail := func(e string) model.ProbeResult { return model.ProbeResult{Success: 0, ErrorType: e} }

	// success beats failure regardless of order
	if betterConnect(fail("x"), ok(99, 99)).Success != 1 {
		t.Fatal("success must beat failure (b)")
	}
	if betterConnect(ok(99, 99), fail("x")).Success != 1 {
		t.Fatal("success must beat failure (a)")
	}
	// lower ttfb wins between successes
	if got := betterConnect(ok(50, 10), ok(20, 99)); got.TTFBMS != 20 {
		t.Fatalf("lower ttfb must win, got %d", got.TTFBMS)
	}
	// tie on ttfb -> lower connect_ms wins
	if got := betterConnect(ok(20, 30), ok(20, 5)); got.ConnectMS != 5 {
		t.Fatalf("tie ttfb -> lower connect wins, got %d", got.ConnectMS)
	}
	// two failures -> keep first (still Down)
	if got := betterConnect(fail("a"), fail("b")); got.Success != 0 || got.ErrorType != "a" {
		t.Fatalf("two failures keep first, got %+v", got)
	}
}

// stratifiedSample always includes the origin floor, samples at most
// groupsPerCycle non-origin groups, and takes exactly one endpoint per group.
func TestStratifiedSample(t *testing.T) {
	eps := []Endpoint{
		{Target: "origin:8080", Path: "/connect", Group: "origin"},
		{Target: "a1", Group: "ga"}, {Target: "a2", Group: "ga"},
		{Target: "b1", Group: "gb"},
		{Target: "c1", Group: "gc"},
		{Target: "d1", Group: "gd"},
		{Target: "e1", Group: "ge"},
		{Target: "f1", Group: "gf"},
	}
	valid := map[string]bool{"ga": true, "gb": true, "gc": true, "gd": true, "ge": true, "gf": true}
	for i := 0; i < 300; i++ {
		s := stratifiedSample(eps, 3)
		origins := 0
		seen := map[string]int{}
		for _, e := range s {
			if e.Group == "origin" {
				origins++
				continue
			}
			if !valid[e.Group] {
				t.Fatalf("sampled unknown group %q", e.Group)
			}
			seen[e.Group]++
		}
		if origins != 1 {
			t.Fatalf("origin must always be probed exactly once, got %d", origins)
		}
		if len(seen) > 3 {
			t.Fatalf("at most 3 non-origin groups per cycle, got %d", len(seen))
		}
		for g, n := range seen {
			if n != 1 {
				t.Fatalf("exactly one endpoint per group; %s had %d", g, n)
			}
		}
	}
}

// When groupsPerCycle >= the number of groups, every group is probed.
func TestStratifiedSampleAllWhenFew(t *testing.T) {
	eps := []Endpoint{
		{Target: "o", Group: "origin"},
		{Target: "a", Group: "ga"},
		{Target: "b", Group: "gb"},
	}
	if got := len(stratifiedSample(eps, 5)); got != 3 {
		t.Fatalf("origin + 2 groups should give 3 endpoints, got %d", got)
	}
}

// ConnectBest returns the winning row under scenario 'connect' plus one
// per-target row per sampled endpoint under 'connect_probe', and accepts any
// 2xx (so a 204 connectivity-check endpoint counts as reachable).
func TestConnectBestProbeRowsAnd2xx(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gen204" {
			w.WriteHeader(http.StatusNoContent) // 204, connectivity-check style
			return
		}
		w.Write([]byte("ok")) // 200
	}))
	defer origin.Close()
	oh := strings.TrimPrefix(origin.URL, "http://")

	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u:p"))
	px, _ := url.Parse("http://u:p@" + tinyConnectProxy(t, auth))

	eps := []Endpoint{
		{Target: oh, Path: "/connect", Group: "origin"},
		{Target: oh, Path: "/gen204", Group: "checkprovider"},
	}
	best, probes := ConnectBest(px, eps, 3*time.Second)

	if best.Success != 1 || best.Scenario != "connect" {
		t.Fatalf("best must be a reachable 'connect' row, got %+v", best)
	}
	if len(probes) != 2 {
		t.Fatalf("expected 2 per-target rows, got %d", len(probes))
	}
	for _, p := range probes {
		if p.Scenario != "connect_probe" {
			t.Fatalf("per-target row scenario = %q, want connect_probe", p.Scenario)
		}
		if p.Success != 1 {
			t.Fatalf("both targets should succeed (200 and 204), got %+v", p)
		}
	}
}
