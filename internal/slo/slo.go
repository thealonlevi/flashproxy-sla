// Package slo holds the SLO evaluation shared by the website (rendering status)
// and the worker's monitor (firing alerts) — so both judge "operational /
// degraded / down" identically.
package slo

import (
	"context"
	"fmt"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/chstore"
)

type SLO struct {
	ConnectMsMedianWarn float64 `json:"connect_ms_median_warn"`
	ConnectMsMedianCrit float64 `json:"connect_ms_median_crit"`
	SuccessPctWarn      float64 `json:"success_pct_warn"`
	SuccessPctCrit      float64 `json:"success_pct_crit"`
	StaleSeconds        int64   `json:"stale_seconds"`
}

// Status is the current rollup + verdict for one package (last 10 minutes).
type Status struct {
	Package         string  `json:"package"`
	Status          string  `json:"status"`
	ConnectMsAvg    float64 `json:"connect_ms_avg"`
	ConnectMsMedian float64 `json:"connect_ms_median"`
	ConnectMsP95    float64 `json:"connect_ms_p95"`
	SuccessPct      float64 `json:"success_pct"`
	Samples         float64 `json:"samples"`
	AgeSeconds      int64   `json:"age_seconds"`
}

// StatusSQL rolls up the connect scenario per package over the last 10 minutes.
// All numeric outputs are <64-bit so ClickHouse renders them as JSON numbers.
func StatusSQL(db string) string {
	return fmt.Sprintf(`SELECT package,
  round(avg(connect_ms), 1)            AS connect_ms_avg,
  round(quantile(0.5)(connect_ms), 1)  AS connect_ms_median,
  round(quantile(0.95)(connect_ms), 1) AS connect_ms_p95,
  round(100 * sum(success) / count(), 2) AS success_pct,
  toUInt32(count())                    AS samples,
  toUInt32(toUnixTimestamp(max(ts)))   AS last_seen_unix
FROM %s.probe_raw
WHERE scenario = 'connect' AND ts > now() - INTERVAL 10 MINUTE
GROUP BY package ORDER BY package`, db)
}

// BarsSQL returns per-package, per-minute buckets for the uptime bars.
func BarsSQL(db string, windowMin int) string {
	return fmt.Sprintf(`SELECT package,
  toUInt32(toUnixTimestamp(toStartOfMinute(ts))) AS t,
  round(quantile(0.5)(connect_ms), 1)    AS median,
  round(100 * sum(success) / count(), 2) AS success_pct,
  toUInt32(count())                      AS samples
FROM %s.probe_raw
WHERE scenario = 'connect' AND ts > now() - INTERVAL %d MINUTE
GROUP BY package, t ORDER BY package, t`, db, windowMin)
}

// Eval maps metrics to a status. ageSec>stale => no_data; pass 0 for historical
// buckets where staleness is irrelevant.
func Eval(median, success float64, ageSec int64, s SLO) string {
	if ageSec > s.StaleSeconds {
		return "no_data"
	}
	if success < s.SuccessPctCrit || median > s.ConnectMsMedianCrit {
		return "down"
	}
	if success < s.SuccessPctWarn || median > s.ConnectMsMedianWarn {
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

// Fetch runs StatusSQL and returns the per-package current status.
func Fetch(ctx context.Context, ch *chstore.Client, db string, s SLO) ([]Status, error) {
	rows, err := ch.QueryJSON(ctx, StatusSQL(db))
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := make([]Status, 0, len(rows))
	for _, m := range rows {
		age := now - int64(chstore.Num(m, "last_seen_unix"))
		out = append(out, Status{
			Package:         chstore.Str(m, "package"),
			Status:          Eval(chstore.Num(m, "connect_ms_median"), chstore.Num(m, "success_pct"), age, s),
			ConnectMsAvg:    chstore.Num(m, "connect_ms_avg"),
			ConnectMsMedian: chstore.Num(m, "connect_ms_median"),
			ConnectMsP95:    chstore.Num(m, "connect_ms_p95"),
			SuccessPct:      chstore.Num(m, "success_pct"),
			Samples:         chstore.Num(m, "samples"),
			AgeSeconds:      age,
		})
	}
	return out, nil
}
