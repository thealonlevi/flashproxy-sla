// Package chstore is a tiny stdlib-only ClickHouse client over the HTTP
// interface. It is the reference implementation of the storage backend; the same
// surface (InsertProbes / QueryJSON / InsertEvent) can be implemented for
// Postgres or SQLite so open-source adopters can swap stores.
package chstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/model"
)

const chTime = "2006-01-02 15:04:05"

type Client struct {
	base string
	db   string
	user string
	pass string
	hc   *http.Client
}

func New(base, db, user, pass string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		db:   db,
		user: user,
		pass: pass,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) exec(ctx context.Context, query string, body io.Reader) ([]byte, error) {
	u := c.base + "/?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ClickHouse-User", c.user)
	req.Header.Set("X-ClickHouse-Key", c.pass)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

// InsertProbes appends probe rows via JSONEachRow.
func (c *Client) InsertProbes(ctx context.Context, rs []model.ProbeResult) error {
	if len(rs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rs {
		_ = enc.Encode(map[string]any{
			"ts":              r.TS.UTC().Format(chTime),
			"vantage":         r.Vantage,
			"package":         r.Package,
			"scenario":        r.Scenario,
			"proto":           r.Proto,
			"target":          r.Target,
			"ip_version":      r.IPVersion,
			"success":         r.Success,
			"error_type":      r.ErrorType,
			"dial_ms":         r.DialMS,
			"connect_ms":      r.ConnectMS,
			"ttfb_ms":         r.TTFBMS,
			"total_ms":        r.TotalMS,
			"bytes":           r.Bytes,
			"throughput_mbps": r.ThroughputMbps,
		})
	}
	_, err := c.exec(ctx, fmt.Sprintf("INSERT INTO %s.probe_raw FORMAT JSONEachRow", c.db), &buf)
	return err
}

// InsertEvent records a deploy/maintenance/status-change marker.
func (c *Client) InsertEvent(ctx context.Context, typ, pkg, msg string) error {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"ts":      time.Now().UTC().Format(chTime),
		"type":    typ,
		"package": pkg,
		"message": msg,
	})
	_, err := c.exec(ctx, fmt.Sprintf("INSERT INTO %s.events FORMAT JSONEachRow", c.db), &buf)
	return err
}

// QueryJSON runs a SELECT and returns the rows of the ClickHouse JSON format.
func (c *Client) QueryJSON(ctx context.Context, sql string) ([]map[string]any, error) {
	b, err := c.exec(ctx, sql+" FORMAT JSON", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode clickhouse json: %w", err)
	}
	return out.Data, nil
}

// Num extracts a float from a ClickHouse JSON value (number, or numeric string
// — 64-bit ints come back as strings to preserve precision).
func Num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case string:
		x, _ := strconv.ParseFloat(v, 64)
		return x
	}
	return 0
}

// Str extracts a string from a ClickHouse JSON value.
func Str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
