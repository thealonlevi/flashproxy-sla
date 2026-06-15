// Package slo holds the SLO evaluation shared by the website (rendering status +
// uptime) and the worker's monitor (firing alerts) — so both judge "operational /
// degraded / down / no_data" identically, and identically to the published SLA.
//
// The definitions mirror the SLA contract (web/sla.html.tmpl §2) EXACTLY:
//   - Available  : the connect scenario succeeds (CONNECT -> 200).
//   - Down       : a minute whose connect success rate is below DownSuccessPct.
//   - Degraded   : Available, but the best (lowest-latency) vantage's AVERAGE
//     connect latency exceeds DegradedAvgMs for DegradedForMin
//     CONSECUTIVE minutes, and stays Degraded until the average
//     recovers at/below the threshold. The run-up minutes before the
//     threshold is met are not yet Degraded ("for N consecutive
//     minutes" is a trigger, evaluated non-retroactively).
//   - Impact     : Down minutes + ½ × Degraded minutes (½-weight).
//   - Availability% = (minutes_with_data − Impact) / minutes_with_data × 100.
//
// These thresholds are published at /api/meta so the verdict is reproducible from
// data alone — not just from this source.
package slo

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/chstore"
)

// SLO are the published thresholds.
type SLO struct {
	DegradedAvgMs  float64 `json:"degraded_avg_ms"`  // avg connect-ms above which a minute is over threshold
	DegradedForMin int     `json:"degraded_for_min"` // consecutive over-threshold minutes that trigger Degraded
	DownSuccessPct float64 `json:"down_success_pct"` // connect success% below which a minute is Down
	StaleSeconds   int64   `json:"stale_seconds"`    // age of newest data beyond which status is no_data
}

func (s SLO) withDefaults() SLO {
	if s.DegradedAvgMs <= 0 {
		s.DegradedAvgMs = 50
	}
	if s.DegradedForMin <= 0 {
		s.DegradedForMin = 5
	}
	if s.DownSuccessPct <= 0 {
		s.DownSuccessPct = 90
	}
	if s.StaleSeconds <= 0 {
		s.StaleSeconds = 120
	}
	return s
}

// Status is the current rollup + verdict for one package (optionally one vantage).
type Status struct {
	Package      string  `json:"package"`
	Vantage      string  `json:"vantage"`
	Status       string  `json:"status"`
	ConnectMsAvg float64 `json:"connect_ms_avg"` // avg over the most recent data minute
	SuccessPct   float64 `json:"success_pct"`
	Samples      float64 `json:"samples"`
	AgeSeconds   int64   `json:"age_seconds"`
}

// Minute is one per-minute rollup of the connect scenario.
type Minute struct {
	T            int64
	ConnectMsAvg float64
	SuccessPct   float64
	Samples      float64
}

// MinutesSQL returns per-(package, vantage, minute) avg connect-ms and success% for
// the connect scenario over the window. This is the single source the status,
// uptime bars, and alerts all derive from.
func MinutesSQL(db string, windowMin int) string {
	return fmt.Sprintf(`SELECT package, vantage,
  toUInt32(toUnixTimestamp(toStartOfMinute(ts))) AS t,
  round(avg(connect_ms), 1)              AS connect_ms_avg,
  round(100 * sum(success) / count(), 2) AS success_pct,
  toUInt32(count())                      AS samples
FROM %s.probe_raw
WHERE scenario = 'connect' AND ts > now() - INTERVAL %d MINUTE
GROUP BY package, vantage, t ORDER BY package, vantage, t`, db, windowMin)
}

// Evaluator classifies a CONTIGUOUS, time-ordered sequence of minutes for one
// (package, vantage), carrying the consecutive-over-threshold run across calls.
type Evaluator struct {
	run int // consecutive over-threshold minutes so far
	s   SLO
}

func NewEvaluator(s SLO) *Evaluator { return &Evaluator{s: s.withDefaults()} }

// Next classifies one minute. hasData=false marks a gap (which breaks the
// consecutive run). It returns operational/degraded/down/no_data.
func (e *Evaluator) Next(hasData bool, connectMsAvg, successPct float64) string {
	if !hasData {
		e.run = 0
		return "no_data"
	}
	if successPct < e.s.DownSuccessPct {
		e.run = 0
		return "down"
	}
	if connectMsAvg > e.s.DegradedAvgMs {
		e.run++
		if e.run >= e.s.DegradedForMin {
			return "degraded"
		}
		return "operational" // over threshold but trigger not yet met
	}
	e.run = 0
	return "operational"
}

// Grid returns windowMin contiguous minute-start unix timestamps ending at the start
// of the current minute.
func Grid(now time.Time, windowMin int) []int64 {
	end := now.Unix()
	end -= end % 60
	g := make([]int64, windowMin)
	for i := range g {
		g[i] = end - int64(windowMin-1-i)*60
	}
	return g
}

// Eval is the simple boundary classifier used for a single already-aggregated
// minute where the consecutive rule does not apply (kept for callers that only need
// down/degraded-by-instant/operational). Prefer Evaluator for sequences.
func Eval(connectMsAvg, successPct float64, ageSec int64, s SLO) string {
	s = s.withDefaults()
	if ageSec > s.StaleSeconds {
		return "no_data"
	}
	if successPct < s.DownSuccessPct {
		return "down"
	}
	if connectMsAvg > s.DegradedAvgMs {
		return "degraded"
	}
	return "operational"
}

// Overall reduces per-component statuses to the headline banner state + label.
func Overall(statuses []string) (string, string) {
	rank := map[string]int{"no_data": 0, "operational": 1, "degraded": 2, "down": 3}
	worst := "no_data"
	for _, s := range statuses {
		if rank[s] > rank[worst] {
			worst = s
		}
	}
	label := map[string]string{
		"operational": "All Systems Operational",
		"degraded":    "Partial System Degradation",
		"down":        "Major System Outage",
		"no_data":     "Awaiting Data",
	}[worst]
	return worst, label
}

// Bar is one classified minute for the uptime strip.
type Bar struct {
	T            int64   `json:"t"`
	Status       string  `json:"status"`
	ConnectMsAvg float64 `json:"connect_ms_avg"`
	SuccessPct   float64 `json:"success_pct"`
	Samples      float64 `json:"samples"`
}

// VantageRollup is one (package, vantage) over the window: classified bars, the
// current status, and the ½-weight availability%.
type VantageRollup struct {
	Vantage   string
	Current   Status
	Bars      []Bar
	UptimePct float64
}

// rollupSeries classifies one (package, vantage) minute series over the grid with
// the consecutive-degraded rule, computing the bars, current status (verdict at the
// most recent data minute, or no_data if stale), and Availability% (Impact = Down +
// ½·Degraded; uptime = (withData − Impact) / withData × 100).
func rollupSeries(byMinute map[int64]Minute, grid []int64, now int64, s SLO) VantageRollup {
	s = s.withDefaults()
	ev := NewEvaluator(s)
	bars := make([]Bar, len(grid))
	var withData, down, deg int
	var lastDataT int64 = -1
	cur := Status{Status: "no_data"}
	for i, t := range grid {
		m, ok := byMinute[t]
		status := ev.Next(ok, m.ConnectMsAvg, m.SuccessPct)
		bars[i] = Bar{T: t, Status: status, ConnectMsAvg: m.ConnectMsAvg, SuccessPct: m.SuccessPct, Samples: m.Samples}
		if ok {
			withData++
			lastDataT = t
			cur = Status{Status: status, ConnectMsAvg: m.ConnectMsAvg, SuccessPct: m.SuccessPct, Samples: m.Samples}
			switch status {
			case "down":
				down++
			case "degraded":
				deg++
			}
		}
	}
	if lastDataT < 0 {
		cur.AgeSeconds = now - grid[0]
	} else {
		cur.AgeSeconds = now - lastDataT
		if cur.AgeSeconds > s.StaleSeconds {
			cur.Status = "no_data"
		}
	}
	uptime := 100.0
	if withData > 0 {
		uptime = (float64(withData) - float64(down) - 0.5*float64(deg)) / float64(withData) * 100
		uptime = float64(int(uptime*100+0.5)) / 100
	}
	return VantageRollup{Current: cur, Bars: bars, UptimePct: uptime}
}

// Rollup returns, per package (sorted), the per-vantage rollups over windowMin. This
// is THE shared implementation of the SLA's status + uptime accounting, used by the
// website (bars/banner) and reduced by Fetch for the monitor.
func Rollup(ctx context.Context, ch *chstore.Client, db string, windowMin int, s SLO) (map[string][]VantageRollup, []string, error) {
	s = s.withDefaults()
	data, err := fetchMinutes(ctx, ch, db, windowMin)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	grid := Grid(now, windowMin)
	out := map[string][]VantageRollup{}
	pkgs := sortedKeys(data)
	for _, pkg := range pkgs {
		for _, van := range sortedKeys(data[pkg]) {
			vr := rollupSeries(data[pkg][van], grid, now.Unix(), s)
			vr.Vantage = van
			vr.Current.Package, vr.Current.Vantage = pkg, van
			out[pkg] = append(out[pkg], vr)
		}
	}
	return out, pkgs, nil
}

// fetchMinutes pulls the per-(package, vantage) minute series for the window.
func fetchMinutes(ctx context.Context, ch *chstore.Client, db string, windowMin int) (map[string]map[string]map[int64]Minute, error) {
	rows, err := ch.QueryJSON(ctx, MinutesSQL(db, windowMin))
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]map[int64]Minute{} // package -> vantage -> t -> Minute
	for _, m := range rows {
		pkg, van := chstore.Str(m, "package"), chstore.Str(m, "vantage")
		if out[pkg] == nil {
			out[pkg] = map[string]map[int64]Minute{}
		}
		if out[pkg][van] == nil {
			out[pkg][van] = map[int64]Minute{}
		}
		t := int64(chstore.Num(m, "t"))
		out[pkg][van][t] = Minute{
			T: t, ConnectMsAvg: chstore.Num(m, "connect_ms_avg"),
			SuccessPct: chstore.Num(m, "success_pct"), Samples: chstore.Num(m, "samples"),
		}
	}
	return out, nil
}

// FetchByVantage returns the current status per (package, vantage).
func FetchByVantage(ctx context.Context, ch *chstore.Client, db string, s SLO) ([]Status, error) {
	s = s.withDefaults()
	rollups, pkgs, err := Rollup(ctx, ch, db, s.DegradedForMin+5, s)
	if err != nil {
		return nil, err
	}
	var out []Status
	for _, pkg := range pkgs {
		for _, vr := range rollups[pkg] {
			out = append(out, vr.Current)
		}
	}
	return out, nil
}

// Fetch returns the current status per package, judged from its BEST vantage (lowest
// recent average connect-ms among vantages with data) — matching the SLA's
// "best vantage" rule. Used by the monitor.
func Fetch(ctx context.Context, ch *chstore.Client, db string, s SLO) ([]Status, error) {
	byVantage, err := FetchByVantage(ctx, ch, db, s)
	if err != nil {
		return nil, err
	}
	best := map[string]Status{}
	order := []string{}
	for _, v := range byVantage {
		cur, ok := best[v.Package]
		if !ok {
			order = append(order, v.Package)
			best[v.Package] = v
			continue
		}
		if betterVantage(v, cur) {
			best[v.Package] = v
		}
	}
	sort.Strings(order)
	out := make([]Status, 0, len(order))
	for _, p := range order {
		st := best[p]
		st.Vantage = "" // package-level rollup
		out = append(out, st)
	}
	return out, nil
}

// betterVantage: real data beats no_data, then lower avg connect, then higher success.
func betterVantage(a, b Status) bool {
	aData, bData := a.Status != "no_data", b.Status != "no_data"
	if aData != bData {
		return aData
	}
	if a.ConnectMsAvg != b.ConnectMsAvg {
		return a.ConnectMsAvg < b.ConnectMsAvg
	}
	return a.SuccessPct > b.SuccessPct
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
