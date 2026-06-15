// Package model holds the wire/storage types and their CANONICAL serialization —
// the byte-exact form the integrity ledger hashes and that an independent verifier
// re-derives from the public ClickHouse to confirm nothing was tampered with.
package model

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// ProbeResult is one synthetic probe attempt. It is produced by the prober and
// stored verbatim in ClickHouse (sla.probe_raw).
//
// connect_ms is the headline SLA metric: the time for the proxy to establish the
// upstream connection (CONNECT -> 200), i.e. what a customer experiences as "time
// to connect through the proxy". dial_ms is the separate client->proxy TCP
// handshake, kept distinct so we can tell a proxy-side regression from a
// network-path regression.
//
// Stream/Seq tie the row to the append-only integrity ledger: Stream is the
// worker's vantage id, Seq is the monotonic batch number the row was flushed in.
type ProbeResult struct {
	TS             time.Time `json:"ts"`
	Vantage        string    `json:"vantage"`
	Package        string    `json:"package"`
	Scenario       string    `json:"scenario"`
	Proto          string    `json:"proto"`
	Target         string    `json:"target"`
	IPVersion      uint8     `json:"ip_version"`
	Success        uint8     `json:"success"`
	ErrorType      string    `json:"error_type"`
	DialMS         uint32    `json:"dial_ms"`
	ConnectMS      uint32    `json:"connect_ms"`
	TTFBMS         uint32    `json:"ttfb_ms"`
	TotalMS        uint32    `json:"total_ms"`
	Bytes          uint64    `json:"bytes"`
	ThroughputMbps float32   `json:"throughput_mbps"`
	Stream         string    `json:"stream"`
	Seq            uint64    `json:"seq"`
}

// Event is a self-recorded marker (deploy / maintenance / status change), stored in
// sla.events and chained into the ledger under stream="events".
type Event struct {
	TS      time.Time `json:"ts"`
	Type    string    `json:"type"`
	Package string    `json:"package"`
	Message string    `json:"message"`
	Stream  string    `json:"stream"`
	Seq     uint64    `json:"seq"`
}

// QuantizeMbps rounds throughput to 3 decimals. The worker stores the quantized
// value, so a verifier reading the stored Float32 back and formatting it the same
// way reproduces the exact canonical string — eliminating float round-trip drift.
func QuantizeMbps(v float32) float32 {
	return float32(math.Round(float64(v)*1000) / 1000)
}

// Canonical is the byte-exact serialization a ledger batch_hash commits to. It is
// deliberately simple and self-describing so an independent verifier can reproduce
// it from the public probe_raw columns: pipe-joined fields, ts as UTC unix seconds,
// throughput quantized to 3 decimals. Stream/Seq are NOT included — they are
// committed by the ledger entry itself, not the row content.
//
// Field order is FROZEN: changing it breaks verification of all historical data.
func (r ProbeResult) Canonical() string {
	return strings.Join([]string{
		strconv.FormatInt(r.TS.UTC().Unix(), 10),
		r.Vantage,
		r.Package,
		r.Scenario,
		r.Proto,
		r.Target,
		strconv.FormatUint(uint64(r.IPVersion), 10),
		strconv.FormatUint(uint64(r.Success), 10),
		r.ErrorType,
		strconv.FormatUint(uint64(r.DialMS), 10),
		strconv.FormatUint(uint64(r.ConnectMS), 10),
		strconv.FormatUint(uint64(r.TTFBMS), 10),
		strconv.FormatUint(uint64(r.TotalMS), 10),
		strconv.FormatUint(r.Bytes, 10),
		strconv.FormatFloat(float64(QuantizeMbps(r.ThroughputMbps)), 'f', 3, 32),
	}, "|")
}

// Canonical for an event: ts|event|type|package|message.
func (e Event) Canonical() string {
	return strings.Join([]string{
		strconv.FormatInt(e.TS.UTC().Unix(), 10),
		"event",
		e.Type,
		e.Package,
		e.Message,
	}, "|")
}
