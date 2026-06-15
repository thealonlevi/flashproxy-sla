// Command website serves the public status page and JSON API. It is STRICTLY
// READ-ONLY: it connects to ClickHouse as flashproxy-status-website (sla_reader
// role) and never writes. Workers write the data; the website only renders it.
// It also publishes the public read-only ClickHouse credentials at /api/meta so
// anyone can reproduce the dashboard from the same data.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/chstore"
	"github.com/flashproxy/flashproxy-status/internal/slo"
)

type chConn struct {
	URL      string `json:"url"`
	DB       string `json:"db"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// publicCH is what we advertise at /api/meta — the published read-only key.
type publicCH struct {
	URL  string `json:"url"`
	DB   string `json:"db"`
	User string `json:"user"`
	// Password is the PUBLIC key, intentionally published.
	Password string `json:"password"`
	Note     string `json:"note"`
}

type Config struct {
	Listen           string   `json:"listen"`
	WebDir           string   `json:"web_dir"`
	ClickHouse       chConn   `json:"clickhouse"`
	PublicClickHouse publicCH `json:"public_clickhouse"`
	SLO              slo.SLO  `json:"slo"`
}

var pkgRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

type server struct {
	cfg Config
	ch  *chstore.Client
}

func main() {
	cfgPath := flag.String("config", "config/website.json", "config path")
	flag.Parse()

	cfg := loadConfig(*cfgPath)
	s := &server{cfg: cfg, ch: chstore.New(cfg.ClickHouse.URL, cfg.ClickHouse.DB, cfg.ClickHouse.User, cfg.ClickHouse.Password)}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/series", s.handleSeries)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/meta", s.handleMeta)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.Handle("/", noCache(http.FileServer(http.Dir(cfg.WebDir))))

	log.Printf("website (read-only) listening on %s as ch-user=%q", cfg.Listen, cfg.ClickHouse.User)
	log.Fatal(http.ListenAndServe(cfg.Listen, mux))
}

func loadConfig(p string) Config {
	b, err := os.ReadFile(p)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	b = []byte(os.ExpandEnv(string(b)))
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		log.Fatalf("parse config: %v", err)
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.WebDir == "" {
		c.WebDir = "web"
	}
	if c.SLO.StaleSeconds == 0 {
		c.SLO.StaleSeconds = 120
	}
	return c
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	rows, err := slo.Fetch(r.Context(), s.ch, s.cfg.ClickHouse.DB, s.cfg.SLO)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"generated_at": nowRFC(), "packages": rows})
}

type barPoint struct {
	T          int64   `json:"t"`
	Status     string  `json:"status"`
	Median     float64 `json:"median"`
	SuccessPct float64 `json:"success_pct"`
	Samples    float64 `json:"samples"`
}

type component struct {
	slo.Status
	UptimePct float64    `json:"uptime_pct"`
	Bars      []barPoint `json:"bars"`
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	const windowMin = 90
	ctx := r.Context()
	cur, err := slo.Fetch(ctx, s.ch, s.cfg.ClickHouse.DB, s.cfg.SLO)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := s.ch.QueryJSON(ctx, slo.BarsSQL(s.cfg.ClickHouse.DB, windowMin))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	idx := map[string]map[int64]barPoint{}
	for _, m := range rows {
		p := chstore.Str(m, "package")
		if idx[p] == nil {
			idx[p] = map[int64]barPoint{}
		}
		t := int64(chstore.Num(m, "t"))
		idx[p][t] = barPoint{
			T: t, Median: chstore.Num(m, "median"),
			SuccessPct: chstore.Num(m, "success_pct"), Samples: chstore.Num(m, "samples"),
			Status: slo.Eval(chstore.Num(m, "median"), chstore.Num(m, "success_pct"), 0, s.cfg.SLO),
		}
	}

	now := time.Now().Unix()
	gridEnd := now - now%60
	grid := make([]int64, windowMin)
	for i := range grid {
		grid[i] = gridEnd - int64(windowMin-1-i)*60
	}

	comps := make([]component, 0, len(cur))
	statuses := make([]string, 0, len(cur))
	for _, ps := range cur {
		bars := make([]barPoint, windowMin)
		var withData, ok int
		for i, t := range grid {
			if b, found := idx[ps.Package][t]; found {
				bars[i] = b
				withData++
				if b.Status == "operational" {
					ok++
				}
			} else {
				bars[i] = barPoint{T: t, Status: "no_data"}
			}
		}
		up := 100.0
		if withData > 0 {
			up = float64(ok) / float64(withData) * 100
		}
		up = float64(int(up*100+0.5)) / 100
		comps = append(comps, component{Status: ps, UptimePct: up, Bars: bars})
		statuses = append(statuses, ps.Status)
	}

	status, label := slo.Overall(statuses)
	writeJSON(w, map[string]any{
		"generated_at":   nowRFC(),
		"window_minutes": windowMin,
		"bucket_seconds": 60,
		"overall":        map[string]string{"status": status, "label": label},
		"components":     comps,
	})
}

func (s *server) handleSeries(w http.ResponseWriter, r *http.Request) {
	pkg := r.URL.Query().Get("package")
	if !pkgRe.MatchString(pkg) {
		http.Error(w, "bad package", http.StatusBadRequest)
		return
	}
	mins := 360
	if v := r.URL.Query().Get("minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 60*24*30 {
			mins = n
		}
	}
	sql := fmt.Sprintf(`SELECT toUInt32(toUnixTimestamp(toStartOfMinute(ts))) AS t,
  round(quantile(0.5)(connect_ms), 1)  AS median,
  round(avg(connect_ms), 1)            AS avg,
  round(quantile(0.95)(connect_ms), 1) AS p95,
  round(100 * sum(success) / count(), 2) AS success_pct
FROM %s.probe_raw
WHERE scenario = 'connect' AND package = '%s' AND ts > now() - INTERVAL %d MINUTE
GROUP BY t ORDER BY t`, s.cfg.ClickHouse.DB, pkg, mins)
	rows, err := s.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"package": pkg, "minutes": mins, "points": rows})
}

// handleEvents is GET-only here (read-only website). Markers are written by the
// worker monitor / CI using the writer role.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rows, err := s.ch.QueryJSON(r.Context(), fmt.Sprintf(
		`SELECT toUInt32(toUnixTimestamp(ts)) AS t, type, package, message FROM %s.events ORDER BY ts DESC LIMIT 100`,
		s.cfg.ClickHouse.DB))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": rows})
}

func (s *server) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"public_clickhouse": s.cfg.PublicClickHouse})
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(v)
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }
