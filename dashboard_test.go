package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDashboardPublicAssetsAndExactAmounts(t *testing.T) {
	bc := NewBlockchain()
	a, b := testWallet(31), testWallet(32)
	mustMine(t, bc, a)
	if err := bc.AddTx(signed(t, a, b.Address, 1, 1)); err != nil {
		t.Fatal(err)
	}
	mustMine(t, bc, a)
	handler := newHandler(bc, a.Address, false, time.Minute)
	for _, path := range []string{"/", "/styles.css", "/app.js", "/stats"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/.gochain-wallet.json", "/gochain-v2.json", "/wallet.go", "/web/index.html", "/unknown"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("private/unknown path %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/explorer/blocks", nil))
	var result struct {
		Tip    string          `json:"tip_hash"`
		Blocks []explorerBlock `json:"blocks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Tip != bc.Stats().TipHash || result.Blocks[2].Txs[0].Amount != "0.00000001" {
		t.Fatalf("inexact explorer projection: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), a.Seed) {
		t.Fatal("seed exposed")
	}
}

func TestMiningMonitorScheduleAndShutdown(t *testing.T) {
	bc := NewBlockchain()
	monitor := &miningMonitor{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go mineLoopMonitored(ctx, bc, testWallet(33).Address, time.Hour, monitor, done)
	deadline := time.Now().Add(10 * time.Second)
	for monitor.snapshot().NextAttempt.IsZero() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond * 10)
	}
	s := monitor.snapshot()
	if s.LastBlock != 1 || s.Active || !s.NextAttempt.After(time.Now()) || s.LastAttempt.IsZero() {
		t.Errorf("invalid schedule: %+v", s)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("miner did not stop")
	}
	if s := monitor.snapshot(); s.Active || !s.NextAttempt.IsZero() {
		t.Fatalf("stale schedule: %+v", s)
	}
}
