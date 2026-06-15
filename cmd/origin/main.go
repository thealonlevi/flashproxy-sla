// Command origin is the deterministic, self-hosted upstream the probers target.
// It serves reproducible payloads so connect-ms / throughput are pure SLA signal
// with no third-party variance. Bind it dual-stack (":8080" listens on v4 and v6)
// so the ipv6 / ipv6-datacenter packages actually exercise v6 egress.
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address (use :PORT for dual-stack v4+v6)")
	flag.Parse()

	mux := http.NewServeMux()

	// /connect — instant 200, the connect-ms probe target.
	mux.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Origin", "flashproxy-status")
		io.WriteString(w, "ok")
	})

	// /small — a few KB, mimics the hi-freq small-payload archetype.
	mux.HandleFunc("/small", func(w http.ResponseWriter, r *http.Request) {
		w.Write(deterministic(4096))
	})

	// /bytes/{n} — exactly n deterministic bytes, for streaming/large-object.
	mux.HandleFunc("/bytes/", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/bytes/"))
		if err != nil || n < 0 || n > 1<<30 {
			http.Error(w, "bad size", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(n))
		writeDeterministic(w, n)
	})

	// /hold?ms=N — trickles bytes for N ms, for the long-session archetype.
	mux.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		if ms <= 0 || ms > 300000 {
			ms = 60000
		}
		fl, _ := w.(http.Flusher)
		deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
		for time.Now().Before(deadline) {
			io.WriteString(w, "x")
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(500 * time.Millisecond)
		}
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })

	log.Printf("origin listening on %s", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func deterministic(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('A' + i%26)
	}
	return b
}

func writeDeterministic(w io.Writer, n int) {
	chunk := deterministic(32 * 1024)
	for n > 0 {
		k := n
		if k > len(chunk) {
			k = len(chunk)
		}
		if _, err := w.Write(chunk[:k]); err != nil {
			return
		}
		n -= k
	}
}
