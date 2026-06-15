// Command worker runs the synthetic scenarios from one vantage and writes results
// DIRECTLY to ClickHouse as flashproxy-status-worker (sla_writer role). Run as
// many workers as you like — N VMs per package, or N VMs each covering every
// package — they all append to the shared ClickHouse. Set "monitor": true on
// exactly one worker to also evaluate SLO status and emit alerts/events.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/chstore"
	"github.com/flashproxy/flashproxy-status/internal/model"
	"github.com/flashproxy/flashproxy-status/internal/probe"
	"github.com/flashproxy/flashproxy-status/internal/slo"
)

type Target struct {
	Package       string `json:"package"`
	ProxyURL      string `json:"proxy_url"`
	ConnectTarget string `json:"connect_target"`
	OriginGet     string `json:"origin_get"`
	IPVersion     uint8  `json:"ip_version"`
	IntervalMS    int    `json:"interval_ms"`
}

type chConn struct {
	URL      string `json:"url"`
	DB       string `json:"db"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// ScenarioCfg holds intervals/sizes for the archetype scenarios (defaults applied
// in loadConfig). The payload scenarios (streaming/large_object/hifreq/long_session)
// run only when Origin is set; scraping/connect always run.
type ScenarioCfg struct {
	StreamingIntervalMS   int `json:"streaming_interval_ms"`
	StreamingBytes        int `json:"streaming_bytes"`
	LargeObjectIntervalMS int `json:"large_object_interval_ms"`
	LargeObjectBytes      int `json:"large_object_bytes"`
	HifreqIntervalMS      int `json:"hifreq_interval_ms"`
	HifreqCount           int `json:"hifreq_count"`
	ScrapingIntervalMS    int `json:"scraping_interval_ms"`
	LongSessionIntervalMS int `json:"long_session_interval_ms"`
	LongSessionHoldMS     int `json:"long_session_hold_ms"`
	PingIntervalMS        int `json:"ping_interval_ms"`
	PingCount             int `json:"ping_count"`
}

type Config struct {
	Vantage     string      `json:"vantage"`
	TimeoutMS   int         `json:"timeout_ms"`
	Monitor     bool        `json:"monitor"`
	Discord     string      `json:"discord_webhook"`
	ClickHouse  chConn      `json:"clickhouse"`
	SLO         slo.SLO     `json:"slo"`
	Origin      string      `json:"origin"`       // host:port of a self-hosted origin (IPv4 path) for payload scenarios
	OriginIPv6  string      `json:"origin_ipv6"`  // dual-stack/v6 origin for ipv6 packages; empty => they skip payload scenarios
	ScrapeHosts []string    `json:"scrape_hosts"` // CONNECT targets for the scraping scenario
	Scenarios   ScenarioCfg `json:"scenarios"`
	Targets     []Target    `json:"targets"`
}

func defInt(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

func main() {
	cfgPath := flag.String("config", "config/worker.json", "config path")
	flag.Parse()

	cfg := loadConfig(*cfgPath)
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = 8000
	}
	ch := chstore.New(cfg.ClickHouse.URL, cfg.ClickHouse.DB, cfg.ClickHouse.User, cfg.ClickHouse.Password)
	log.Printf("worker vantage=%q targets=%d ch-user=%q monitor=%v", cfg.Vantage, len(cfg.Targets), cfg.ClickHouse.User, cfg.Monitor)

	results := make(chan model.ProbeResult, 1024)
	go flusher(ch, results)
	for _, t := range cfg.Targets {
		go runTarget(cfg, t, results)
	}
	if cfg.Monitor {
		go monitorLoop(cfg, ch)
	}
	select {}
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
	s := &c.Scenarios
	s.StreamingIntervalMS = defInt(s.StreamingIntervalMS, 120000)
	s.StreamingBytes = defInt(s.StreamingBytes, 5*1024*1024)
	s.LargeObjectIntervalMS = defInt(s.LargeObjectIntervalMS, 60000)
	s.LargeObjectBytes = defInt(s.LargeObjectBytes, 262144)
	s.HifreqIntervalMS = defInt(s.HifreqIntervalMS, 60000)
	s.HifreqCount = defInt(s.HifreqCount, 10)
	s.ScrapingIntervalMS = defInt(s.ScrapingIntervalMS, 60000)
	s.LongSessionIntervalMS = defInt(s.LongSessionIntervalMS, 180000)
	s.LongSessionHoldMS = defInt(s.LongSessionHoldMS, 20000)
	s.PingIntervalMS = defInt(s.PingIntervalMS, 15000)
	s.PingCount = defInt(s.PingCount, 3)
	if len(c.ScrapeHosts) == 0 {
		// all dual-stack so ipv6-egress packages can reach them too
		c.ScrapeHosts = []string{
			"www.google.com:443", "www.cloudflare.com:443", "www.facebook.com:443",
			"www.wikipedia.org:443", "www.microsoft.com:443",
		}
	}
	return c
}

func runTarget(cfg Config, t Target, out chan<- model.ProbeResult) {
	proxy, err := url.Parse(t.ProxyURL)
	if err != nil || proxy.Host == "" {
		log.Printf("[%s] bad proxy_url %q: %v", t.Package, t.ProxyURL, err)
		return
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	streamTimeout := 60 * time.Second // big downloads need a generous deadline

	emit := func(rs ...model.ProbeResult) {
		for i := range rs {
			rs[i].Vantage = cfg.Vantage
			rs[i].Package = t.Package
			rs[i].IPVersion = t.IPVersion
			out <- rs[i]
		}
	}
	// loop runs fn immediately, then every intervalMs.
	loop := func(intervalMs int, fn func()) {
		if intervalMs <= 0 {
			return
		}
		tk := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
		defer tk.Stop()
		for {
			fn()
			<-tk.C
		}
	}

	s := cfg.Scenarios
	// connect (the SLA headline) + scraping always run.
	go loop(defInt(t.IntervalMS, 20000), func() {
		emit(probe.ConnectScenario(proxy, t.ConnectTarget, t.OriginGet, timeout))
	})
	go loop(s.ScrapingIntervalMS, func() {
		emit(probe.Scraping(proxy, cfg.ScrapeHosts, timeout)...)
	})
	// ICMP ping to the gateway host — raw network RTT, runs alongside everything.
	go loop(s.PingIntervalMS, func() {
		emit(probe.Ping(proxy.Hostname(), s.PingCount, timeout))
	})
	// payload scenarios need a self-hosted origin reachable over the package's IP
	// family. ipv6 packages egress over v6, so they need a dual-stack origin.
	origin := cfg.Origin
	if t.IPVersion == 6 {
		origin = cfg.OriginIPv6
	}
	if origin != "" {
		go loop(s.StreamingIntervalMS, func() {
			emit(probe.Streaming(proxy, origin, s.StreamingBytes, streamTimeout))
		})
		go loop(s.LargeObjectIntervalMS, func() {
			emit(probe.LargeObject(proxy, origin, s.LargeObjectBytes, timeout))
		})
		go loop(s.HifreqIntervalMS, func() {
			emit(probe.HifreqSmall(proxy, origin, s.HifreqCount, timeout)...)
		})
		go loop(s.LongSessionIntervalMS, func() {
			emit(probe.LongSession(proxy, origin, s.LongSessionHoldMS, timeout))
		})
	}
}

// flusher batches probe rows so ClickHouse gets fewer, larger inserts.
func flusher(ch *chstore.Client, in <-chan model.ProbeResult) {
	buf := make([]model.ProbeResult, 0, 256)
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	flush := func() {
		if len(buf) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := ch.InsertProbes(ctx, buf); err != nil {
			log.Printf("insert %d rows: %v", len(buf), err)
		} else {
			log.Printf("flushed %d probes", len(buf))
		}
		cancel()
		buf = buf[:0]
	}
	for {
		select {
		case r := <-in:
			buf = append(buf, r)
			if len(buf) >= 200 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

// monitorLoop evaluates SLO status every 30s and records a status_change event +
// Discord alert on transition. Enable on exactly one worker.
func monitorLoop(cfg Config, ch *chstore.Client) {
	last := map[string]string{}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		rows, err := slo.Fetch(ctx, ch, cfg.ClickHouse.DB, cfg.SLO)
		cancel()
		if err != nil {
			log.Printf("monitor: %v", err)
			continue
		}
		for _, p := range rows {
			prev := last[p.Package]
			last[p.Package] = p.Status
			if prev == "" || prev == p.Status {
				continue
			}
			log.Printf("status change %s: %s -> %s (median %.0fms)", p.Package, prev, p.Status, p.ConnectMsMedian)
			msg := fmt.Sprintf("%s -> %s (connect median %.0fms, %.1f%% ok)", prev, p.Status, p.ConnectMsMedian, p.SuccessPct)
			_ = ch.InsertEvent(context.Background(), "status_change", p.Package, msg)
			if cfg.Discord != "" {
				emoji := map[string]string{"operational": "✅", "degraded": "⚠️", "down": "🔴", "no_data": "⚪"}[p.Status]
				postDiscord(cfg.Discord, fmt.Sprintf("%s **%s**: `%s` → `%s` (connect median %.0fms)", emoji, p.Package, prev, p.Status, p.ConnectMsMedian))
			}
		}
	}
}

func postDiscord(url, content string) {
	body, _ := json.Marshal(map[string]string{"content": content})
	resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("discord: %v", err)
		return
	}
	resp.Body.Close()
}
