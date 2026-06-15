// Package model holds the wire types shared between the prober and collector.
package model

import "time"

// ProbeResult is one synthetic probe attempt. It is produced by the prober,
// shipped to the collector, and stored verbatim in ClickHouse (sla.probe_raw).
//
// connect_ms is the headline SLA metric: the time for the proxy to establish
// the upstream connection (CONNECT -> 200), i.e. what a customer experiences as
// "time to connect through the proxy". dial_ms is the separate client->proxy
// TCP handshake, kept distinct so we can tell a proxy-side regression from a
// network-path regression.
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
}

// Batch is the ingest payload (prober -> collector POST /ingest).
type Batch struct {
	Results []ProbeResult `json:"results"`
}
