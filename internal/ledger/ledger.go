// Package ledger implements the append-only, hash-chained integrity ledger that
// makes the metrics database tamper-EVIDENT: any retroactive edit, deletion, or
// reorder of a covered row or ledger entry becomes detectable by recomputation.
//
// It is intentionally dependency-free (crypto/sha256 + crypto/ed25519 from the
// stdlib) so the exact algorithm is auditable and an independent verifier needs
// nothing but the public data and the published public key.
//
// What it proves: rows present in the published DB form an unbroken hash chain
// consistent with signed checkpoints. What it does NOT prove on its own: that the
// measurements were honest at generation time, nor — with in-DB-only checkpoints —
// that a holder of the signing key did not rewrite history and re-sign it. Closing
// the latter requires anchoring the signed head externally (OpenTimestamps / a
// transparency log / a public git commit); the Checkpoint type is anchor-ready.
package ledger

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// GenesisHash is the prev_hash of the first entry in a stream (64 hex zeros).
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Kinds.
const (
	KindProbe = "probe"
	KindEvent = "event"
)

// hashHex returns the lowercase-hex SHA-256 of b.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BatchHash commits to a set of canonical row strings. Rows are sorted so the hash
// is independent of the order ClickHouse returns them in (a verifier sorts too),
// then joined with '\n' and hashed. row_count is committed separately by the entry.
func BatchHash(canonicalRows []string) string {
	rows := append([]string(nil), canonicalRows...)
	sort.Strings(rows)
	return hashHex([]byte(strings.Join(rows, "\n")))
}

// EntryHash chains an entry: sha256(stream|seq|prev_hash|batch_hash|ts_first|ts_last|row_count).
// All fields are committed so none can be altered without detection.
func EntryHash(stream string, seq uint64, prevHash, batchHash string, tsFirst, tsLast int64, rowCount uint32) string {
	msg := strings.Join([]string{
		stream,
		strconv.FormatUint(seq, 10),
		prevHash,
		batchHash,
		strconv.FormatInt(tsFirst, 10),
		strconv.FormatInt(tsLast, 10),
		strconv.FormatUint(uint64(rowCount), 10),
	}, "|")
	return hashHex([]byte(msg))
}

// Entry is one link in a stream's chain.
type Entry struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Kind      string `json:"kind"`
	TSFirst   int64  `json:"ts_first"`
	TSLast    int64  `json:"ts_last"`
	RowCount  uint32 `json:"row_count"`
	BatchHash string `json:"batch_hash"`
	PrevHash  string `json:"prev_hash"`
	EntryHash string `json:"entry_hash"`
}

// Chain maintains the append position for one stream. It is NOT safe for concurrent
// use — each stream must have a single writer (one worker per vantage), which is
// also what keeps the chain fork-free.
type Chain struct {
	stream string
	seq    uint64 // seq of the last committed entry; 0 means none yet (next is 1)
	prev   string // entry_hash of the last committed entry; GenesisHash if none
}

// NewChain starts a fresh chain at genesis (first Append produces seq 1).
func NewChain(stream string) *Chain {
	return &Chain{stream: stream, seq: 0, prev: GenesisHash}
}

// Resume continues an existing chain from its persisted head, so a restarted worker
// appends seq+1 with the correct prev_hash. Pass the last entry's (seq, entry_hash);
// seq 0 / empty hash means "no prior entries" (treated as genesis).
func Resume(stream string, lastSeq uint64, lastEntryHash string) *Chain {
	if lastSeq == 0 || lastEntryHash == "" {
		return NewChain(stream)
	}
	return &Chain{stream: stream, seq: lastSeq, prev: lastEntryHash}
}

// Head returns the current (seq, prev_hash) append position.
func (c *Chain) Head() (uint64, string) { return c.seq, c.prev }

// Build computes the next entry committing to the given canonical rows WITHOUT
// advancing the chain. Callers persist the entry (and its rows) durably, then call
// Commit — so a failed write can be retried at the same seq instead of corrupting
// the chain. tsFirst/tsLast bound the rows' timestamps (unix seconds, UTC).
func (c *Chain) Build(kind string, canonicalRows []string, tsFirst, tsLast int64) Entry {
	seq := c.seq + 1
	bh := BatchHash(canonicalRows)
	eh := EntryHash(c.stream, seq, c.prev, bh, tsFirst, tsLast, uint32(len(canonicalRows)))
	return Entry{
		Stream: c.stream, Seq: seq, Kind: kind,
		TSFirst: tsFirst, TSLast: tsLast, RowCount: uint32(len(canonicalRows)),
		BatchHash: bh, PrevHash: c.prev, EntryHash: eh,
	}
}

// Commit advances the chain head to a previously-Built entry that has now been
// durably written.
func (c *Chain) Commit(e Entry) { c.seq, c.prev = e.Seq, e.EntryHash }

// Append builds and immediately commits the next entry (convenience for callers
// that don't need the build/commit split, e.g. tests).
func (c *Chain) Append(kind string, canonicalRows []string, tsFirst, tsLast int64) Entry {
	e := c.Build(kind, canonicalRows, tsFirst, tsLast)
	c.Commit(e)
	return e
}

// VerifyEntry recomputes batch_hash (from the rows) and entry_hash and checks they
// match the stored entry and that prev_hash links to the previous entry_hash. Used
// by the standalone verifier. Pass prevEntryHash = GenesisHash for seq 1.
func VerifyEntry(e Entry, canonicalRows []string, prevEntryHash string) error {
	if e.PrevHash != prevEntryHash {
		return fmt.Errorf("stream %s seq %d: prev_hash %s != previous entry_hash %s (chain break)", e.Stream, e.Seq, e.PrevHash, prevEntryHash)
	}
	if canonicalRows != nil { // nil = raw rows expired (>400d); still check chain linkage
		if bh := BatchHash(canonicalRows); bh != e.BatchHash {
			return fmt.Errorf("stream %s seq %d: batch_hash mismatch (rows altered): recomputed %s != stored %s", e.Stream, e.Seq, bh, e.BatchHash)
		}
		if uint32(len(canonicalRows)) != e.RowCount {
			return fmt.Errorf("stream %s seq %d: row_count %d != %d rows present", e.Stream, e.Seq, e.RowCount, len(canonicalRows))
		}
	}
	if eh := EntryHash(e.Stream, e.Seq, e.PrevHash, e.BatchHash, e.TSFirst, e.TSLast, e.RowCount); eh != e.EntryHash {
		return fmt.Errorf("stream %s seq %d: entry_hash mismatch (entry altered): recomputed %s != stored %s", e.Stream, e.Seq, eh, e.EntryHash)
	}
	return nil
}

// ---- Ed25519-signed checkpoints ----

// Checkpoint is a signed attestation of a stream's chain head at a point in time.
// (Anchor-ready: an external-anchor proof can be attached later without changing
// the signed message.)
type Checkpoint struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	EntryHash string `json:"entry_hash"`
	TS        int64  `json:"ts"` // unix seconds, UTC
	PubKeyID  string `json:"pubkey_id"`
	Signature string `json:"signature"` // base64
}

// checkpointMessage is the canonical, signed message: ckpt|stream|seq|entry_hash|ts.
func checkpointMessage(stream string, seq uint64, entryHash string, ts int64) []byte {
	return []byte(strings.Join([]string{
		"ckpt", stream, strconv.FormatUint(seq, 10), entryHash, strconv.FormatInt(ts, 10),
	}, "|"))
}

// SignCheckpoint produces a signed checkpoint for the given head.
func SignCheckpoint(priv ed25519.PrivateKey, stream string, seq uint64, entryHash string, ts int64) Checkpoint {
	sig := ed25519.Sign(priv, checkpointMessage(stream, seq, entryHash, ts))
	return Checkpoint{
		Stream: stream, Seq: seq, EntryHash: entryHash, TS: ts,
		PubKeyID:  PubKeyID(priv.Public().(ed25519.PublicKey)),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
}

// VerifyCheckpoint checks the checkpoint's signature against pub.
func VerifyCheckpoint(pub ed25519.PublicKey, c Checkpoint) error {
	sig, err := base64.StdEncoding.DecodeString(c.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, checkpointMessage(c.Stream, c.Seq, c.EntryHash, c.TS), sig) {
		return fmt.Errorf("stream %s seq %d: checkpoint signature invalid", c.Stream, c.Seq)
	}
	return nil
}

// PubKeyID is a short, stable id for a public key (first 8 hex chars of its bytes),
// recorded on checkpoints so a verifier knows which published key to check against.
func PubKeyID(pub ed25519.PublicKey) string {
	return hex.EncodeToString(pub)[:8]
}

// ---- key (de)serialization ----

// ParsePrivateKey accepts a base64- or hex-encoded Ed25519 seed (32 bytes) or full
// private key (64 bytes) and returns the private key.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := decodeKey(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	default:
		return nil, fmt.Errorf("ed25519 private key must be %d (seed) or %d bytes, got %d", ed25519.SeedSize, ed25519.PrivateKeySize, len(b))
	}
}

// ParsePublicKey accepts a base64- or hex-encoded 32-byte Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := decodeKey(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("ed25519 public key must be %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	return ed25519.PublicKey(b), nil
}

// PublicKeyB64 is the canonical published form of a public key.
func PublicKeyB64(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

func decodeKey(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("key is not valid base64 or hex")
}
