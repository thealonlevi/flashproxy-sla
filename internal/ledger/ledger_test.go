package ledger

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/flashproxy/flashproxy-status/internal/model"
	"time"
)

// canonRows builds a few canonical probe-row strings for a batch.
func canonRows(n int, base time.Time) []string {
	rows := make([]string, n)
	for i := 0; i < n; i++ {
		r := model.ProbeResult{
			TS: base.Add(time.Duration(i) * time.Second), Vantage: "us-east", Package: "isp",
			Scenario: "connect", Proto: "http", Target: "x:443", IPVersion: 4, Success: 1,
			ConnectMS: uint32(40 + i), TotalMS: uint32(41 + i), ThroughputMbps: float32(i) + 0.123,
		}
		rows[i] = r.Canonical()
	}
	return rows
}

func TestChainAppendAndVerify(t *testing.T) {
	c := NewChain("us-east")
	base := time.Unix(1_700_000_000, 0).UTC()

	var entries []Entry
	var batches [][]string
	prev := GenesisHash
	for b := 0; b < 5; b++ {
		rows := canonRows(3, base.Add(time.Duration(b)*time.Minute))
		e := c.Append(KindProbe, rows, rows0ts(rows), rows0ts(rows))
		if e.Seq != uint64(b+1) {
			t.Fatalf("seq: got %d want %d", e.Seq, b+1)
		}
		if e.PrevHash != prev {
			t.Fatalf("prev_hash not linked at seq %d", e.Seq)
		}
		entries = append(entries, e)
		batches = append(batches, rows)
		prev = e.EntryHash
	}

	// A faithful re-verification of the whole chain must pass.
	prev = GenesisHash
	for i, e := range entries {
		if err := VerifyEntry(e, batches[i], prev); err != nil {
			t.Fatalf("clean verify failed at seq %d: %v", e.Seq, err)
		}
		prev = e.EntryHash
	}
}

func TestVerifyDetectsRowTamper(t *testing.T) {
	c := NewChain("eu")
	rows := canonRows(3, time.Unix(1_700_000_000, 0).UTC())
	e := c.Append(KindProbe, rows, 0, 0)

	// Flip one row's content (e.g. an operator hiding a failure).
	tampered := append([]string(nil), rows...)
	tampered[1] = tampered[1] + "X"
	if err := VerifyEntry(e, tampered, GenesisHash); err == nil {
		t.Fatal("expected batch_hash mismatch on altered row, got nil")
	}

	// Deleting a row from a covered batch must also be caught (row_count + hash).
	if err := VerifyEntry(e, rows[:2], GenesisHash); err == nil {
		t.Fatal("expected mismatch on deleted row, got nil")
	}
}

func TestVerifyDetectsChainBreak(t *testing.T) {
	c := NewChain("eu")
	r1 := canonRows(2, time.Unix(1_700_000_000, 0).UTC())
	e1 := c.Append(KindProbe, r1, 0, 0)
	r2 := canonRows(2, time.Unix(1_700_000_100, 0).UTC())
	e2 := c.Append(KindProbe, r2, 0, 0)

	// If e1 is altered, e2.prev_hash no longer matches e1's recomputed entry_hash.
	// Simulate by verifying e2 against a wrong previous hash.
	if err := VerifyEntry(e2, r2, GenesisHash); err == nil {
		t.Fatal("expected chain break when prev entry hash is wrong, got nil")
	}
	// Correct linkage still verifies.
	if err := VerifyEntry(e2, r2, e1.EntryHash); err != nil {
		t.Fatalf("correct linkage failed: %v", err)
	}
}

func TestVerifyDetectsEntryTamper(t *testing.T) {
	c := NewChain("eu")
	rows := canonRows(2, time.Unix(1_700_000_000, 0).UTC())
	e := c.Append(KindProbe, rows, 10, 20)

	// Operator edits the stored entry's metadata (e.g. shifts ts_last) but forgets
	// it can't recompute a matching entry_hash without breaking the chain.
	bad := e
	bad.TSLast = 999999
	if err := VerifyEntry(bad, rows, GenesisHash); err == nil {
		t.Fatal("expected entry_hash mismatch on altered entry, got nil")
	}
}

func TestChainResume(t *testing.T) {
	c := NewChain("v")
	e1 := c.Append(KindProbe, canonRows(1, time.Unix(1, 0)), 0, 0)
	e2 := c.Append(KindProbe, canonRows(1, time.Unix(2, 0)), 0, 0)

	// A restarted worker resumes from the persisted head and continues seq+1.
	resumed := Resume("v", e2.Seq, e2.EntryHash)
	e3 := resumed.Append(KindProbe, canonRows(1, time.Unix(3, 0)), 0, 0)
	if e3.Seq != 3 {
		t.Fatalf("resumed seq: got %d want 3", e3.Seq)
	}
	if e3.PrevHash != e2.EntryHash {
		t.Fatal("resumed chain did not link to persisted head")
	}
	_ = e1
}

func TestCheckpointSignVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cp := SignCheckpoint(priv, "us-east", 42, "deadbeef", 1_700_000_000)
	if cp.PubKeyID != PubKeyID(pub) {
		t.Fatal("pubkey id mismatch")
	}
	if err := VerifyCheckpoint(pub, cp); err != nil {
		t.Fatalf("clean checkpoint failed to verify: %v", err)
	}

	// Tamper with the attested head -> signature must fail.
	bad := cp
	bad.EntryHash = "cafebabe"
	if err := VerifyCheckpoint(pub, bad); err == nil {
		t.Fatal("expected signature failure on altered checkpoint, got nil")
	}

	// A different key must not verify.
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyCheckpoint(pub2, cp); err == nil {
		t.Fatal("expected failure verifying with wrong key, got nil")
	}
}

func TestKeyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	// Base64 seed round-trips.
	seedB64 := PublicKeyB64(pub) // just exercising the encoder for pub
	if _, err := ParsePublicKey(seedB64); err != nil {
		t.Fatalf("parse pub b64: %v", err)
	}
	priv2, err := ParsePrivateKey(b64(priv.Seed()))
	if err != nil {
		t.Fatalf("parse priv seed: %v", err)
	}
	if !priv2.Equal(priv) {
		t.Fatal("private key seed round-trip mismatch")
	}
}

// helpers
func rows0ts(rows []string) int64 { return 0 }
func b64(b []byte) string {
	return PublicKeyB64(ed25519.PublicKey(b)) // base64 std encoding of arbitrary bytes
}
