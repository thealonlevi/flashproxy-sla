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
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	SiteURL          string   `json:"site_url"` // canonical URL for SEO (no trailing slash)
	TLSCertFile      string   `json:"tls_cert_file"`
	TLSKeyFile       string   `json:"tls_key_file"`
	ClickHouse       chConn   `json:"clickhouse"`
	PublicClickHouse publicCH `json:"public_clickhouse"`
	SLO              slo.SLO  `json:"slo"`
}

var pkgRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

type server struct {
	cfg     Config
	ch      *chstore.Client
	tmpl    *template.Template
	slaTmpl *template.Template
	static  http.Handler
}

func main() {
	cfgPath := flag.String("config", "config/website.json", "config path")
	flag.Parse()

	cfg := loadConfig(*cfgPath)
	s := &server{
		cfg:     cfg,
		ch:      chstore.New(cfg.ClickHouse.URL, cfg.ClickHouse.DB, cfg.ClickHouse.User, cfg.ClickHouse.Password),
		tmpl:    template.Must(template.ParseFiles(filepath.Join(cfg.WebDir, "index.html.tmpl"))),
		slaTmpl: template.Must(template.ParseFiles(filepath.Join(cfg.WebDir, "sla.html.tmpl"))),
		static:  http.FileServer(http.Dir(cfg.WebDir)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/series", s.handleSeries)
	mux.HandleFunc("/api/scenarios", s.handleScenarios)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/meta", s.handleMeta)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("/robots.txt", s.handleRobots)
	mux.HandleFunc("/sitemap.xml", s.handleSitemap)
	mux.HandleFunc("/llms.txt", s.handleLLMs)
	mux.HandleFunc("/sla", s.handleSLA)
	mux.HandleFunc("/", s.handleRoot) // SSR for "/", static for everything else

	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		log.Printf("website (read-only, TLS) listening on %s as ch-user=%q", cfg.Listen, cfg.ClickHouse.User)
		log.Fatal(http.ListenAndServeTLS(cfg.Listen, cfg.TLSCertFile, cfg.TLSKeyFile, mux))
	}
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
	if c.SiteURL == "" {
		c.SiteURL = "https://status.flashproxy.com"
	}
	c.SiteURL = strings.TrimRight(c.SiteURL, "/")
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

// vantageView is one package's stats from one vantage (with its own uptime bars).
type vantageView struct {
	Vantage         string     `json:"vantage"`
	Status          string     `json:"status"`
	ConnectMsAvg    float64    `json:"connect_ms_avg"`
	ConnectMsMedian float64    `json:"connect_ms_median"`
	ConnectMsP95    float64    `json:"connect_ms_p95"`
	SuccessPct      float64    `json:"success_pct"`
	Samples         float64    `json:"samples"`
	AgeSeconds      int64      `json:"age_seconds"`
	UptimePct       float64    `json:"uptime_pct"`
	Bars            []barPoint `json:"bars"`
}

// component is one package: headline metrics come from its best vantage, and the
// full per-vantage breakdown is in Vantages so the page can toggle.
type component struct {
	Package         string        `json:"package"`
	Status          string        `json:"status"`
	DefaultVantage  string        `json:"default_vantage"`
	ConnectMsAvg    float64       `json:"connect_ms_avg"`
	ConnectMsMedian float64       `json:"connect_ms_median"`
	ConnectMsP95    float64       `json:"connect_ms_p95"`
	SuccessPct      float64       `json:"success_pct"`
	Samples         float64       `json:"samples"`
	UptimePct       float64       `json:"uptime_pct"`
	Bars            []barPoint    `json:"bars"`
	Vantages        []vantageView `json:"vantages"`
}

// better reports whether vantage a should be preferred over b as the default:
// real data beats no-data, then lowest median connect-ms, then success, then samples.
func better(a, b vantageView) bool {
	aData, bData := a.Status != "no_data", b.Status != "no_data"
	if aData != bData {
		return aData
	}
	if a.ConnectMsMedian != b.ConnectMsMedian {
		return a.ConnectMsMedian < b.ConnectMsMedian
	}
	if a.SuccessPct != b.SuccessPct {
		return a.SuccessPct > b.SuccessPct
	}
	return a.Samples > b.Samples
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	const windowMin = 90
	ctx := r.Context()
	cur, err := slo.FetchByVantage(ctx, s.ch, s.cfg.ClickHouse.DB, s.cfg.SLO)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := s.ch.QueryJSON(ctx, slo.BarsByVantageSQL(s.cfg.ClickHouse.DB, windowMin))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// pkg -> vantage -> minute -> bar
	idx := map[string]map[string]map[int64]barPoint{}
	for _, m := range rows {
		p, v := chstore.Str(m, "package"), chstore.Str(m, "vantage")
		if idx[p] == nil {
			idx[p] = map[string]map[int64]barPoint{}
		}
		if idx[p][v] == nil {
			idx[p][v] = map[int64]barPoint{}
		}
		t := int64(chstore.Num(m, "t"))
		idx[p][v][t] = barPoint{
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

	// group per-vantage statuses by package, preserving first-seen order
	byPkg := map[string][]slo.Status{}
	order := []string{}
	for _, st := range cur {
		if _, ok := byPkg[st.Package]; !ok {
			order = append(order, st.Package)
		}
		byPkg[st.Package] = append(byPkg[st.Package], st)
	}

	comps := make([]component, 0, len(order))
	statuses := make([]string, 0, len(order))
	for _, pkg := range order {
		views := make([]vantageView, 0, len(byPkg[pkg]))
		bestIdx := 0
		for _, st := range byPkg[pkg] {
			bars := make([]barPoint, windowMin)
			var withData, ok int
			for i, t := range grid {
				if b, found := idx[pkg][st.Vantage][t]; found {
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
			views = append(views, vantageView{
				Vantage: st.Vantage, Status: st.Status,
				ConnectMsAvg: st.ConnectMsAvg, ConnectMsMedian: st.ConnectMsMedian,
				ConnectMsP95: st.ConnectMsP95, SuccessPct: st.SuccessPct,
				Samples: st.Samples, AgeSeconds: st.AgeSeconds,
				UptimePct: up, Bars: bars,
			})
			if better(views[len(views)-1], views[bestIdx]) {
				bestIdx = len(views) - 1
			}
		}
		best := views[bestIdx]
		comps = append(comps, component{
			Package:         pkg,
			Status:          best.Status,
			DefaultVantage:  best.Vantage,
			ConnectMsAvg:    best.ConnectMsAvg,
			ConnectMsMedian: best.ConnectMsMedian,
			ConnectMsP95:    best.ConnectMsP95,
			SuccessPct:      best.SuccessPct,
			Samples:         best.Samples,
			UptimePct:       best.UptimePct,
			Bars:            best.Bars,
			Vantages:        views,
		})
		statuses = append(statuses, best.Status)
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
	vantageFilter := ""
	if v := r.URL.Query().Get("vantage"); v != "" {
		if !pkgRe.MatchString(v) {
			http.Error(w, "bad vantage", http.StatusBadRequest)
			return
		}
		vantageFilter = fmt.Sprintf(" AND vantage = '%s'", v)
	}
	// gateway ping vs proxy connect, as separate time series
	sql := fmt.Sprintf(`SELECT scenario,
  toUInt32(toUnixTimestamp(toStartOfMinute(ts))) AS t,
  round(quantile(0.5)(connect_ms), 1) AS median
FROM %s.probe_raw
WHERE scenario IN ('connect', 'ping') AND package = '%s'%s AND ts > now() - INTERVAL %d MINUTE
GROUP BY scenario, t ORDER BY scenario, t`, s.cfg.ClickHouse.DB, pkg, vantageFilter, mins)
	rows, err := s.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	series := map[string][]map[string]any{}
	for _, m := range rows {
		sc := chstore.Str(m, "scenario")
		series[sc] = append(series[sc], map[string]any{"t": chstore.Num(m, "t"), "median": chstore.Num(m, "median")})
	}
	writeJSON(w, map[string]any{"package": pkg, "minutes": mins, "series": series})
}

// handleScenarios returns per-archetype-scenario stats for one package (optionally
// one vantage) over the last 10 minutes.
func (s *server) handleScenarios(w http.ResponseWriter, r *http.Request) {
	pkg := r.URL.Query().Get("package")
	if !pkgRe.MatchString(pkg) {
		http.Error(w, "bad package", http.StatusBadRequest)
		return
	}
	vf := ""
	if v := r.URL.Query().Get("vantage"); v != "" {
		if !pkgRe.MatchString(v) {
			http.Error(w, "bad vantage", http.StatusBadRequest)
			return
		}
		vf = fmt.Sprintf(" AND vantage = '%s'", v)
	}
	sql := fmt.Sprintf(`SELECT scenario,
  toUInt32(count())                      AS samples,
  round(100 * sum(success) / count(), 2) AS success_pct,
  round(quantile(0.5)(connect_ms), 1)    AS connect_ms_median,
  round(quantile(0.5)(ttfb_ms), 1)       AS ttfb_ms_median,
  round(avg(throughput_mbps), 1)         AS throughput_mbps_avg,
  round(quantile(0.5)(total_ms), 1)      AS total_ms_median
FROM %s.probe_raw
WHERE package = '%s'%s AND ts > now() - INTERVAL 10 MINUTE
GROUP BY scenario ORDER BY scenario`, s.cfg.ClickHouse.DB, pkg, vf)
	rows, err := s.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"package": pkg, "scenarios": rows})
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

// --- SEO / GEO: server-side rendered home + crawler files ---

type idxProduct struct {
	Name        string
	Status      string // operational|degraded|down|no_data
	StatusLabel string
	Median      int
	Vantage     string
}

type idxData struct {
	SiteURL       string
	OverallStatus string
	OverallLabel  string
	Updated       string
	Products      []idxProduct
	JSONLD        template.JS
}

// jsonLD is the schema.org structured data (Organization + WebSite + FAQPage) for
// SEO + GEO. Built as a string and injected raw (template.HTML) so html/template
// doesn't mangle the JSON inside the <script>.
const jsonLD = `[
{"@context":"https://schema.org","@type":"Organization","name":"FlashProxy","url":"https://flashproxy.com","description":"High-performance HTTP and SOCKS5 proxies with Shared ISP, Datacenter, IPv6 Datacenter, and IPv6 Residential IP options."},
{"@context":"https://schema.org","@type":"WebSite","name":"FlashProxy Status","url":"https://status.flashproxy.com"},
{"@context":"https://schema.org","@type":"FAQPage","mainEntity":[
{"@type":"Question","name":"Is FlashProxy operational right now?","acceptedAnswer":{"@type":"Answer","text":"FlashProxy's live operational status, uptime, and latency are published on status.flashproxy.com and updated continuously from US and EU vantage points."}},
{"@type":"Question","name":"What proxy products does FlashProxy offer?","acceptedAnswer":{"@type":"Answer","text":"FlashProxy offers Shared ISP proxies (USA and EU), Datacenter proxies, IPv6 Datacenter proxies, and IPv6 Residential proxies, over HTTP and SOCKS5."}},
{"@type":"Question","name":"How is FlashProxy uptime and latency measured?","acceptedAnswer":{"@type":"Answer","text":"Synthetic probes run continuously from multiple regions, measuring proxy connect latency, gateway ping RTT, throughput, success rate, and a direct no-proxy baseline for each product. The methodology is open source."}}
]}
]`

var statusLabel = map[string]string{
	"operational": "Operational", "degraded": "Degraded",
	"down": "Down", "no_data": "No data",
}

// slaJSONLD is structured data for the /sla page (Organization + WebPage + FAQ).
const slaJSONLD = `[
{"@context":"https://schema.org","@type":"Organization","name":"FlashProxy","url":"https://flashproxy.com"},
{"@context":"https://schema.org","@type":"WebPage","name":"FlashProxy Service Level Agreement","url":"https://status.flashproxy.com/sla","description":"FlashProxy's 100% uptime guarantee with automatic, proportional compensation, independently verifiable via open-source monitoring and a public metrics database."},
{"@context":"https://schema.org","@type":"FAQPage","mainEntity":[
{"@type":"Question","name":"Does FlashProxy offer an uptime SLA?","acceptedAnswer":{"@type":"Answer","text":"Yes. FlashProxy guarantees 100% availability on Datacenter, IPv6 Datacenter, Shared ISP USA, IPv6 Residential, and Shared ISP EU, with automatic, proportional compensation for any qualifying downtime or degradation."}},
{"@type":"Question","name":"How does FlashProxy compensate for downtime?","acceptedAnswer":{"@type":"Answer","text":"Per-GB plans receive automatic account credits scaled to monthly availability (10% below 99.9%, up to 100% below 90%). Time-based Unlimited plans receive an automatic SLA credit based on total plan cost, at 5x to 10x the value of the lost time, capped at 100% of the plan cost. Degradation (best-vantage connect latency above 50ms for 5 consecutive minutes) counts at half weight."}},
{"@type":"Question","name":"Is FlashProxy's SLA independently verifiable?","acceptedAnswer":{"@type":"Answer","text":"Yes. The monitoring system is fully open source at github.com/thealonlevi/flashproxy-sla, and the ClickHouse metrics database that powers status.flashproxy.com is publicly readable, so anyone can reproduce every availability figure."}}
]}
]`

// buildIndex computes a current snapshot (best vantage per product) for SSR.
func (s *server) buildIndex(r *http.Request) idxData {
	d := idxData{SiteURL: s.cfg.SiteURL, OverallStatus: "no_data", OverallLabel: "Awaiting Data", Updated: "Updated " + time.Now().UTC().Format("2006-01-02 15:04 MST"), JSONLD: template.JS(jsonLD)}
	cur, err := slo.FetchByVantage(r.Context(), s.ch, s.cfg.ClickHouse.DB, s.cfg.SLO)
	if err != nil {
		return d
	}
	type best struct {
		st  slo.Status
		set bool
	}
	byPkg := map[string]*best{}
	order := []string{}
	for _, st := range cur {
		b := byPkg[st.Package]
		if b == nil {
			b = &best{}
			byPkg[st.Package] = b
			order = append(order, st.Package)
		}
		better := !b.set ||
			(st.Status != "no_data" && b.st.Status == "no_data") ||
			(st.Status != "no_data" && b.st.Status != "no_data" && st.ConnectMsMedian < b.st.ConnectMsMedian)
		if better {
			b.st = st
			b.set = true
		}
	}
	sort.Strings(order)
	statuses := []string{}
	for _, pkg := range order {
		st := byPkg[pkg].st
		d.Products = append(d.Products, idxProduct{
			Name: pkg, Status: st.Status, StatusLabel: statusLabel[st.Status],
			Median: int(st.ConnectMsMedian + 0.5), Vantage: strings.TrimPrefix(st.Vantage, "aws-"),
		})
		statuses = append(statuses, st.Status)
	}
	if len(statuses) > 0 {
		d.OverallStatus, d.OverallLabel = slo.Overall(statuses)
	}
	return d
}

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		noCache(s.static).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30")
	if err := s.tmpl.Execute(w, s.buildIndex(r)); err != nil {
		log.Printf("render index: %v", err)
	}
}

func (s *server) handleSLA(w http.ResponseWriter, r *http.Request) {
	d := s.buildIndex(r)
	d.JSONLD = template.JS(slaJSONLD)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if err := s.slaTmpl.Execute(w, d); err != nil {
		log.Printf("render sla: %v", err)
	}
}

func (s *server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, `User-agent: *
Allow: /

# AI / answer engines (explicitly welcomed for GEO)
User-agent: GPTBot
Allow: /
User-agent: OAI-SearchBot
Allow: /
User-agent: ChatGPT-User
Allow: /
User-agent: ClaudeBot
Allow: /
User-agent: PerplexityBot
Allow: /
User-agent: Google-Extended
Allow: /
User-agent: CCBot
Allow: /

Sitemap: %s/sitemap.xml
`, s.cfg.SiteURL)
}

func (s *server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	today := time.Now().UTC().Format("2006-01-02")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/</loc><lastmod>%s</lastmod><changefreq>hourly</changefreq><priority>1.0</priority></url>
  <url><loc>%s/sla</loc><lastmod>%s</lastmod><changefreq>monthly</changefreq><priority>0.9</priority></url>
</urlset>
`, s.cfg.SiteURL, today, s.cfg.SiteURL, today)
}

func (s *server) handleLLMs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	d := s.buildIndex(r)
	fmt.Fprintf(w, "# FlashProxy Status\n\n")
	fmt.Fprintf(w, "> Live uptime and latency for FlashProxy's proxy network, measured continuously by synthetic probes from multiple global vantage points (US and EU).\n\n")
	fmt.Fprintf(w, "Current overall status: %s (%s).\n\n", d.OverallLabel, d.Updated)
	fmt.Fprintf(w, "## Products monitored\n\n")
	for _, p := range d.Products {
		fmt.Fprintf(w, "- %s: %s, ~%dms median connect (best vantage: %s)\n", p.Name, p.StatusLabel, p.Median, p.Vantage)
	}
	fmt.Fprintf(w, "\n## What is measured\n\nPer product, per vantage: average and median connect latency (the time the proxy takes to establish the upstream connection), gateway ping RTT, throughput (streaming and large-object), high-frequency small-payload setup, broad scraping reachability, long-session stability, and a direct (no-proxy) baseline for comparison.\n\n")
	fmt.Fprintf(w, "## SLA\n\nFlashProxy backs these products with a 100%% uptime guarantee and automatic, proportional compensation. Full terms: %s/sla\n\n", s.cfg.SiteURL)
	fmt.Fprintf(w, "## Source\n\nThis status page is open source and fully reproducible: %s — and the metrics database is publicly readable.\n", "https://github.com/thealonlevi/flashproxy-sla")
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
