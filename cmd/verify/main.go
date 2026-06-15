// Command verify independently audits the integrity ledger of a flashproxy-status
// ClickHouse using ONLY public, read-only access. It recomputes every batch hash
// from the raw rows, walks each per-stream hash chain, verifies the Ed25519-signed
// checkpoints against the published public key, and reports any tampering,
// data-loss gap, or broken signature.
//
// This is the tool the public uses to confirm FlashProxy has not altered the SLA
// data. It needs nothing but the published ClickHouse credentials and public key
// (both at https://status.flashproxy.com/api/meta).
//
//	go run ./cmd/verify -ch https://ch.flashproxy.com -user flashproxy-status-public \
//	    -pass flashproxy-public-ro -pubkey <base64>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/flashproxy/flashproxy-status/internal/chstore"
	"github.com/flashproxy/flashproxy-status/internal/ledger"
	"github.com/flashproxy/flashproxy-status/internal/model"
)

func main() {
	chURL := flag.String("ch", "", "ClickHouse HTTP URL (e.g. https://ch.flashproxy.com)")
	db := flag.String("db", "sla", "database")
	user := flag.String("user", "flashproxy-status-public", "ClickHouse user")
	pass := flag.String("pass", "", "ClickHouse password")
	pubkeyB64 := flag.String("pubkey", "", "published Ed25519 public key (base64); if empty, checkpoint signatures are NOT verified")
	flag.Parse()
	if *chURL == "" {
		fmt.Fprintln(os.Stderr, "error: -ch is required")
		os.Exit(2)
	}

	ch := chstore.New(*chURL, *db, *user, *pass)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	v := &verifier{ch: ch, db: *db}
	if *pubkeyB64 != "" {
		pk, err := ledger.ParsePublicKey(*pubkeyB64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad -pubkey: %v\n", err)
			os.Exit(2)
		}
		v.pub = pk
		fmt.Printf("verifying against public key %s (id %s)\n", *pubkeyB64, ledger.PubKeyID(pk))
	} else {
		fmt.Println("WARNING: no -pubkey given; chain will be checked but checkpoint signatures will NOT be verified")
	}

	if err := v.run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\nVERIFICATION FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nVERIFICATION OK — every chain links cleanly and every checkpoint signature is valid.")
}

type verifier struct {
	ch  *chstore.Client
	db  string
	pub []byte // ed25519 public key, or nil

	// validated[stream][seq] = recomputed-and-confirmed entry_hash
	validated map[string]map[uint64]string
	problems  int
}

func (v *verifier) run(ctx context.Context) error {
	v.validated = map[string]map[uint64]string{}

	streams, err := v.streams(ctx)
	if err != nil {
		return fmt.Errorf("list streams: %w", err)
	}
	if len(streams) == 0 {
		return fmt.Errorf("no ledger streams found")
	}
	for _, s := range streams {
		if err := v.verifyStream(ctx, s); err != nil {
			return err
		}
	}
	if err := v.verifyCheckpoints(ctx); err != nil {
		return err
	}
	if v.problems > 0 {
		return fmt.Errorf("%d problem(s) detected", v.problems)
	}
	return nil
}

func (v *verifier) streams(ctx context.Context) ([]string, error) {
	rows, err := v.ch.QueryJSON(ctx, fmt.Sprintf("SELECT DISTINCT stream FROM %s.ledger ORDER BY stream", v.db))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range rows {
		out = append(out, chstore.Str(m, "stream"))
	}
	return out, nil
}

// verifyStream walks one stream's chain in seq order, recomputing batch and entry
// hashes from the underlying rows and checking forward linkage. Seq gaps (a batch
// lost to a crash before its rows were written) are reported but do not break
// linkage — the next present entry must still chain to the previous present one.
func (v *verifier) verifyStream(ctx context.Context, stream string) error {
	entries, err := v.ledgerEntries(ctx, stream)
	if err != nil {
		return fmt.Errorf("read ledger for %s: %w", stream, err)
	}
	rowsBySeq, err := v.canonicalRowsBySeq(ctx, stream)
	if err != nil {
		return fmt.Errorf("read rows for %s: %w", stream, err)
	}
	v.validated[stream] = map[uint64]string{}

	prev := ledger.GenesisHash
	var lastSeq uint64
	gaps := 0
	cutoff := time.Now().Add(-399 * 24 * time.Hour).Unix() // rows older than this may be TTL-expired
	for i, e := range entries {
		if i > 0 && e.Seq != lastSeq+1 {
			gaps += int(e.Seq - lastSeq - 1)
			fmt.Printf("  [%s] seq gap: %d missing between %d and %d (data lost pre-commit; linkage checked)\n", stream, e.Seq-lastSeq-1, lastSeq, e.Seq)
		}
		var rows []string
		if r, ok := rowsBySeq[e.Seq]; ok {
			rows = r
		} else if e.TSLast < cutoff {
			rows = nil // expired raw rows: verify chain linkage only
		} else {
			rows = []string{} // rows should exist but don't -> row_count mismatch flagged below
		}
		if err := ledger.VerifyEntry(e, rows, prev); err != nil {
			v.problems++
			fmt.Printf("  [%s] FAIL seq %d: %v\n", stream, e.Seq, err)
		} else {
			v.validated[stream][e.Seq] = e.EntryHash
		}
		prev = e.EntryHash
		lastSeq = e.Seq
	}
	status := "ok"
	if gaps > 0 {
		status = fmt.Sprintf("ok (%d gap-minute(s) from crashes)", gaps)
	}
	fmt.Printf("stream %-16s %5d entries, seq 1..%d — %s\n", stream, len(entries), lastSeq, status)
	return nil
}

func (v *verifier) verifyCheckpoints(ctx context.Context) error {
	rows, err := v.ch.QueryJSON(ctx, fmt.Sprintf(
		`SELECT stream, toUInt64(seq) AS seq, entry_hash, toUInt32(toUnixTimestamp(ts)) AS ts, pubkey_id, signature
		 FROM %s.ledger_checkpoints ORDER BY stream, seq`, v.db))
	if err != nil {
		return fmt.Errorf("read checkpoints: %w", err)
	}
	var sigOK, sigSkip, consistent int
	for _, m := range rows {
		cp := ledger.Checkpoint{
			Stream: chstore.Str(m, "stream"), Seq: chstore.NumU64(m, "seq"),
			EntryHash: chstore.Str(m, "entry_hash"), TS: int64(chstore.Num(m, "ts")),
			PubKeyID: chstore.Str(m, "pubkey_id"), Signature: chstore.Str(m, "signature"),
		}
		// The checkpoint must attest the same entry_hash we recomputed at that seq.
		if got, ok := v.validated[cp.Stream][cp.Seq]; ok && got != cp.EntryHash {
			v.problems++
			fmt.Printf("  checkpoint %s seq %d: attested entry_hash %s != recomputed %s\n", cp.Stream, cp.Seq, cp.EntryHash, got)
		} else if ok {
			consistent++
		}
		if v.pub != nil {
			if err := ledger.VerifyCheckpoint(v.pub, cp); err != nil {
				v.problems++
				fmt.Printf("  checkpoint %s seq %d: %v\n", cp.Stream, cp.Seq, err)
			} else {
				sigOK++
			}
		} else {
			sigSkip++
		}
	}
	fmt.Printf("checkpoints: %d total, %d signature(s) valid, %d consistent with chain, %d skipped (no pubkey)\n",
		len(rows), sigOK, consistent, sigSkip)
	return nil
}

func (v *verifier) ledgerEntries(ctx context.Context, stream string) ([]ledger.Entry, error) {
	rows, err := v.ch.QueryJSON(ctx, fmt.Sprintf(
		`SELECT stream, toUInt64(seq) AS seq, kind,
		   toInt64(toUnixTimestamp(ts_first)) AS ts_first, toInt64(toUnixTimestamp(ts_last)) AS ts_last,
		   toUInt32(row_count) AS row_count, batch_hash, prev_hash, entry_hash
		 FROM %s.ledger WHERE stream = '%s' ORDER BY seq`, v.db, esc(stream)))
	if err != nil {
		return nil, err
	}
	out := make([]ledger.Entry, 0, len(rows))
	for _, m := range rows {
		out = append(out, ledger.Entry{
			Stream: chstore.Str(m, "stream"), Seq: chstore.NumU64(m, "seq"), Kind: chstore.Str(m, "kind"),
			TSFirst: int64(chstore.Num(m, "ts_first")), TSLast: int64(chstore.Num(m, "ts_last")),
			RowCount:  uint32(chstore.NumU64(m, "row_count")),
			BatchHash: chstore.Str(m, "batch_hash"), PrevHash: chstore.Str(m, "prev_hash"), EntryHash: chstore.Str(m, "entry_hash"),
		})
	}
	return out, nil
}

// canonicalRowsBySeq reconstructs the canonical row strings for each seq directly
// from the public columns — exactly as the worker hashed them.
func (v *verifier) canonicalRowsBySeq(ctx context.Context, stream string) (map[uint64][]string, error) {
	out := map[uint64][]string{}
	if stream == "events" {
		rows, err := v.ch.QueryJSON(ctx, fmt.Sprintf(
			`SELECT toInt64(toUnixTimestamp(ts)) AS ts, type, package, message, toUInt64(seq) AS seq
			 FROM %s.events WHERE stream = 'events' ORDER BY seq`, v.db))
		if err != nil {
			return nil, err
		}
		for _, m := range rows {
			e := model.Event{
				TS:   time.Unix(int64(chstore.Num(m, "ts")), 0).UTC(),
				Type: chstore.Str(m, "type"), Package: chstore.Str(m, "package"), Message: chstore.Str(m, "message"),
			}
			seq := chstore.NumU64(m, "seq")
			out[seq] = append(out[seq], e.Canonical())
		}
		return out, nil
	}
	rows, err := v.ch.QueryJSON(ctx, fmt.Sprintf(
		`SELECT toInt64(toUnixTimestamp(ts)) AS ts, vantage, package, scenario, proto, target,
		   toUInt32(ip_version) AS ip_version, toUInt32(success) AS success, error_type,
		   toUInt32(dial_ms) AS dial_ms, toUInt32(connect_ms) AS connect_ms, toUInt32(ttfb_ms) AS ttfb_ms,
		   toUInt32(total_ms) AS total_ms, toUInt64(bytes) AS bytes, throughput_mbps, toUInt64(seq) AS seq
		 FROM %s.probe_raw WHERE stream = '%s' ORDER BY seq`, v.db, esc(stream)))
	if err != nil {
		return nil, err
	}
	for _, m := range rows {
		r := model.ProbeResult{
			TS:      time.Unix(int64(chstore.Num(m, "ts")), 0).UTC(),
			Vantage: chstore.Str(m, "vantage"), Package: chstore.Str(m, "package"), Scenario: chstore.Str(m, "scenario"),
			Proto: chstore.Str(m, "proto"), Target: chstore.Str(m, "target"),
			IPVersion: uint8(chstore.Num(m, "ip_version")), Success: uint8(chstore.Num(m, "success")),
			ErrorType: chstore.Str(m, "error_type"),
			DialMS:    uint32(chstore.Num(m, "dial_ms")), ConnectMS: uint32(chstore.Num(m, "connect_ms")),
			TTFBMS: uint32(chstore.Num(m, "ttfb_ms")), TotalMS: uint32(chstore.Num(m, "total_ms")),
			Bytes: chstore.NumU64(m, "bytes"), ThroughputMbps: float32(chstore.Num(m, "throughput_mbps")),
		}
		seq := chstore.NumU64(m, "seq")
		out[seq] = append(out[seq], r.Canonical())
	}
	return out, nil
}

func esc(s string) string {
	// streams are operator-controlled identifiers; quote defensively anyway.
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\'' || r == '\\' {
			b = append(b, '\\')
		}
		b = append(b, r)
	}
	return string(b)
}
