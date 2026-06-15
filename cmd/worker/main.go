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

type Config struct {
	Vantage    string   `json:"vantage"`
	TimeoutMS  int      `json:"timeout_ms"`
	Monitor    bool     `json:"monitor"`
	Discord    string   `json:"discord_webhook"`
	ClickHouse chConn   `json:"clickhouse"`
	SLO        slo.SLO  `json:"slo"`
	Targets    []Target `json:"targets"`
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
	return c
}

func runTarget(cfg Config, t Target, out chan<- model.ProbeResult) {
	interval := t.IntervalMS
	if interval <= 0 {
		interval = 20000
	}
	proxy, err := url.Parse(t.ProxyURL)
	if err != nil || proxy.Host == "" {
		log.Printf("[%s] bad proxy_url %q: %v", t.Package, t.ProxyURL, err)
		return
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	defer ticker.Stop()
	for {
		r := probe.ConnectScenario(proxy, t.ConnectTarget, t.OriginGet, timeout)
		r.Vantage = cfg.Vantage
		r.Package = t.Package
		r.IPVersion = t.IPVersion
		out <- r
		<-ticker.C
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := ch.InsertProbes(ctx, buf); err != nil {
			log.Printf("insert %d rows: %v", len(buf), err)
		}
		cancel()
		buf = buf[:0]
	}
	for {
		select {
		case r := <-in:
			buf = append(buf, r)
			log.Printf("[%s] connect_ms=%d dial_ms=%d success=%d err=%q", r.Package, r.ConnectMS, r.DialMS, r.Success, r.ErrorType)
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
