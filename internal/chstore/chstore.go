// Package chstore is a tiny stdlib-only ClickHouse client over the HTTP interface.
// It is the reference implementation of the storage backend; the same surface can be
// implemented for Postgres or SQLite so open-source adopters can swap stores.
//
// Every request pins session_timezone=UTC so the naive DateTime('UTC') values we
// write are interpreted identically regardless of the server's local timezone.
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

	"github.com/flashproxy/flashproxy-status/internal/ledger"
	"github.com/flashproxy/flashproxy-status/internal/model"
)

const chTime = "2006-01-02 15:04:05"

// maxResponseBytes bounds how much of a ClickHouse response we will buffer, so a
// huge or runaway result can't OOM the process. Result sets here are small.
const maxResponseBytes = 256 << 20 // 256 MiB

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

// exec runs a statement. extra carries additional URL settings (e.g. read caps).
func (c *Client) exec(ctx context.Context, query string, body io.Reader, extra url.Values) ([]byte, error) {
	v := url.Values{}
	v.Set("query", query)
	v.Set("session_timezone", "UTC")
	for k, vals := range extra {
		for _, val := range vals {
			v.Add(k, val)
		}
	}
	u := c.base + "/?" + v.Encode()
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
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clickhouse %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if readErr != nil {
		return nil, fmt.Errorf("read clickhouse response: %w", readErr)
	}
	return b, nil
}

// dedupExtra returns URL settings that make a retried insert idempotent: ClickHouse
// drops a re-inserted block with the same token (requires the table's
// non_replicated_deduplication_window setting). Empty token => no dedup.
func dedupExtra(token string) url.Values {
	v := url.Values{}
	if token != "" {
		v.Set("insert_deduplication_token", token)
	}
	return v
}

// InsertProbes appends probe rows via JSONEachRow. Throughput is stored quantized so
// it matches the value the integrity ledger committed to. dedupToken makes retries
// idempotent (pass "<stream>:<seq>").
func (c *Client) InsertProbes(ctx context.Context, rs []model.ProbeResult, dedupToken string) error {
	if len(rs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rs {
		if err := enc.Encode(map[string]any{
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
			"throughput_mbps": model.QuantizeMbps(r.ThroughputMbps),
			"stream":          r.Stream,
			"seq":             r.Seq,
		}); err != nil {
			return fmt.Errorf("encode probe row: %w", err)
		}
	}
	_, err := c.exec(ctx, fmt.Sprintf("INSERT INTO %s.probe_raw FORMAT JSONEachRow", c.db), &buf, dedupExtra(dedupToken))
	return err
}

// InsertEvent records a deploy/maintenance/status-change marker.
func (c *Client) InsertEvent(ctx context.Context, e model.Event) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]any{
		"ts":      e.TS.UTC().Format(chTime),
		"type":    e.Type,
		"package": e.Package,
		"message": e.Message,
		"stream":  e.Stream,
		"seq":     e.Seq,
	}); err != nil {
		return err
	}
	_, err := c.exec(ctx, fmt.Sprintf("INSERT INTO %s.events FORMAT JSONEachRow", c.db), &buf, nil)
	return err
}

// InsertLedger appends one integrity-ledger entry. dedupToken makes retries
// idempotent (pass "<stream>:<seq>:ledger").
func (c *Client) InsertLedger(ctx context.Context, e ledger.Entry, dedupToken string) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]any{
		"stream":     e.Stream,
		"seq":        e.Seq,
		"kind":       e.Kind,
		"ts_first":   time.Unix(e.TSFirst, 0).UTC().Format(chTime),
		"ts_last":    time.Unix(e.TSLast, 0).UTC().Format(chTime),
		"row_count":  e.RowCount,
		"batch_hash": e.BatchHash,
		"prev_hash":  e.PrevHash,
		"entry_hash": e.EntryHash,
	}); err != nil {
		return err
	}
	_, err := c.exec(ctx, fmt.Sprintf("INSERT INTO %s.ledger FORMAT JSONEachRow", c.db), &buf, dedupExtra(dedupToken))
	return err
}

// InsertCheckpoint appends one signed checkpoint.
func (c *Client) InsertCheckpoint(ctx context.Context, cp ledger.Checkpoint) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]any{
		"stream":     cp.Stream,
		"seq":        cp.Seq,
		"entry_hash": cp.EntryHash,
		"ts":         time.Unix(cp.TS, 0).UTC().Format(chTime),
		"pubkey_id":  cp.PubKeyID,
		"signature":  cp.Signature,
	}); err != nil {
		return err
	}
	_, err := c.exec(ctx, fmt.Sprintf("INSERT INTO %s.ledger_checkpoints FORMAT JSONEachRow", c.db), &buf, nil)
	return err
}

// LedgerHead returns the last (seq, entry_hash) for a stream so a restarted worker
// can resume its chain. Returns (0, "") when the stream has no entries yet.
func (c *Client) LedgerHead(ctx context.Context, stream string) (uint64, string, error) {
	rows, err := c.QueryJSON(ctx, fmt.Sprintf(
		"SELECT seq, entry_hash FROM %s.ledger WHERE stream = '%s' ORDER BY seq DESC LIMIT 1",
		c.db, escapeLit(stream)))
	if err != nil {
		return 0, "", err
	}
	if len(rows) == 0 {
		return 0, "", nil
	}
	return uint64(Num(rows[0], "seq")), Str(rows[0], "entry_hash"), nil
}

// ProbeMaxSeq returns the highest seq already present in probe_raw for a stream, so
// a restarted worker never reuses a seq that a pre-crash insert may have written
// without its ledger entry. Returns 0 if none.
func (c *Client) ProbeMaxSeq(ctx context.Context, stream string) (uint64, error) {
	rows, err := c.QueryJSON(ctx, fmt.Sprintf(
		"SELECT toUInt64(max(seq)) AS s FROM %s.probe_raw WHERE stream = '%s'",
		c.db, escapeLit(stream)))
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	return NumU64(rows[0], "s"), nil
}

// LedgerHeads returns the current head (seq, entry_hash) of every stream — used by
// the checkpoint signer to attest all chains, including other workers' vantages.
func (c *Client) LedgerHeads(ctx context.Context) (map[string]struct {
	Seq       uint64
	EntryHash string
}, error) {
	rows, err := c.QueryJSON(ctx, fmt.Sprintf(
		"SELECT stream, toUInt64(max(seq)) AS seq, argMax(entry_hash, seq) AS entry_hash FROM %s.ledger GROUP BY stream",
		c.db))
	if err != nil {
		return nil, err
	}
	out := map[string]struct {
		Seq       uint64
		EntryHash string
	}{}
	for _, m := range rows {
		out[Str(m, "stream")] = struct {
			Seq       uint64
			EntryHash string
		}{NumU64(m, "seq"), Str(m, "entry_hash")}
	}
	return out, nil
}

// QueryJSON runs a SELECT and returns the rows of the ClickHouse JSON format.
func (c *Client) QueryJSON(ctx context.Context, sql string) ([]map[string]any, error) {
	extra := url.Values{}
	extra.Set("cancel_http_readonly_queries_on_client_close", "1")
	b, err := c.exec(ctx, sql+" FORMAT JSON", nil, extra)
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

// escapeLit escapes a string for use inside a single-quoted ClickHouse literal.
// Callers should still validate identifiers; this is defense-in-depth.
func escapeLit(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `'`, `\'`)
}

// Num extracts a float from a ClickHouse JSON value (number, or numeric string —
// 64-bit ints come back as strings to preserve precision). NOTE: do not use for
// UInt64 totals that may exceed 2^53; use NumU64 instead.
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

// NumU64 extracts a uint64 without float precision loss (ClickHouse renders UInt64
// as a JSON string for exactly this reason).
func NumU64(m map[string]any, k string) uint64 {
	switch v := m[k].(type) {
	case string:
		x, _ := strconv.ParseUint(v, 10, 64)
		return x
	case float64:
		return uint64(v)
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
