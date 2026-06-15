// Command keygen generates an Ed25519 keypair for signing integrity-ledger
// checkpoints. Put the printed private seed in the monitor worker's
// LEDGER_SIGNING_KEY, and publish the public key as the website's LEDGER_PUBKEY.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/flashproxy/flashproxy-status/internal/ledger"
)

func main() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
	fmt.Println("# Ed25519 keypair for the flashproxy-status integrity ledger.")
	fmt.Println("# Keep the PRIVATE seed secret (worker LEDGER_SIGNING_KEY); publish the PUBLIC key.")
	fmt.Printf("LEDGER_SIGNING_KEY=%s\n", base64.StdEncoding.EncodeToString(priv.Seed()))
	fmt.Printf("LEDGER_PUBKEY=%s\n", ledger.PublicKeyB64(pub))
	fmt.Printf("# pubkey_id=%s\n", ledger.PubKeyID(pub))
}
