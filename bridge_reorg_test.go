package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Explicit opt-in: owns an isolated Go chain and creates disposable Solana assets.
// The fork trigger exists only in this test handler, never in a production node.
func TestBridgeRealReorganization(t *testing.T) {
	if os.Getenv("GO_BRIDGE_REORG") != "1" {
		t.Skip("set GO_BRIDGE_REORG=1 for local cross-chain integration")
	}
	if !CheckpointRequired {
		t.Skip("requires checkpoint build")
	}
	bc := NewBlockchain()
	cert := signedCheckpoint(t, bc.Snapshot(), time.Now().Unix(), testWallet(99))
	bc.checkpoint = &cert
	normal := newHandler(bc, testWallet(98).Address, false, time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test/reorg" {
			normal.ServeHTTP(w, r)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		fork := NewBlockchain()
		fork.checkpoint = &cert
		count := len(bc.Blocks()) + 1
		for i := 0; i < count; i++ {
			if _, err := fork.MinePending(testWallet(97).Address); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		adopted, err := bc.Adopt(fork.Snapshot())
		if err != nil || !adopted {
			http.Error(w, "fork adoption failed", 500)
			return
		}
		writeJSON(w, map[string]bool{"adopted": true})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--import", "tsx", "scripts/verify-bridge-reorg.ts", "--isolated", server.URL)
	cmd.Dir = filepath.Join(".", "solana-devnet")
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
