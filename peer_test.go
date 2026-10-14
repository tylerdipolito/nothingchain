package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPeerPropagationAndCatchup(t *testing.T) {
	a, b := testWallet(41), testWallet(42)
	first := NewBlockchain()
	second, err := LoadOrCreate(filepath.Join(t.TempDir(), "second.json"))
	if err != nil {
		t.Fatal(err)
	}
	one := httptest.NewServer(newHandler(first, a.Address, false, time.Minute))
	defer one.Close()
	two := httptest.NewServer(newHandler(second, b.Address, false, time.Minute))
	defer two.Close()
	pm1, err := newPeerManager(first, []string{two.URL})
	if err != nil {
		t.Fatal(err)
	}
	pm2, err := newPeerManager(second, []string{one.URL})
	if err != nil {
		t.Fatal(err)
	}
	mustMine(t, first, a)
	pm2.SyncOnce(context.Background())
	if first.Stats().TipHash != second.Stats().TipHash {
		t.Fatal("block not propagated")
	}
	tx := signed(t, a, b.Address, UnitsPerGO/4, 1)
	if err := second.AddTx(tx); err != nil {
		t.Fatal(err)
	}
	pm1.SyncOnce(context.Background())
	if first.Stats().PendingTxs != 1 {
		t.Fatal("mempool not propagated")
	}
	mustMine(t, first, a)
	pm2.SyncOnce(context.Background())
	if account(t, second, b).Balance != UnitsPerGO/4 || second.Stats().PendingTxs != 0 {
		t.Fatal("peer did not confirm fractional transfer")
	}
	mustMine(t, first, a)
	mustMine(t, first, a) // second is disconnected during these blocks
	pm2.SyncOnce(context.Background())
	restarted, err := LoadOrCreate(second.path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Stats().TipHash != first.Stats().TipHash {
		t.Fatal("catchup not persisted")
	}
	if status := pm2.Status()[0]; status.LastError != "" || status.AdoptedChains < 1 {
		t.Fatal(status)
	}
}
func TestCompetingBlocksAndOrphanRecovery(t *testing.T) {
	a, b := testWallet(43), testWallet(44)
	left, right := NewBlockchain(), NewBlockchain()
	mustMine(t, left, a)
	if _, err := right.Adopt(left.Snapshot()); err != nil {
		t.Fatal(err)
	}
	tx := signed(t, a, b.Address, 1, 1)
	if err := left.AddTx(tx); err != nil {
		t.Fatal(err)
	}
	mustMine(t, left, a)
	mustMine(t, right, b)
	leftFork, rightFork := left.Snapshot(), right.Snapshot()
	want := left.Stats().TipHash
	if right.Stats().TipHash < want {
		want = right.Stats().TipHash
	}
	if _, err := left.Adopt(rightFork); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Adopt(leftFork); err != nil {
		t.Fatal(err)
	}
	if left.Stats().TipHash != want || right.Stats().TipHash != want {
		t.Fatal("equal-work forks did not converge")
	}
	// Explicitly build a longer fork without the transaction; left must requeue it.
	fork := NewBlockchain()
	base := leftFork
	base.Blocks = base.Blocks[:2]
	base.Pending = nil
	if _, err := fork.Adopt(base); err != nil {
		t.Fatal(err)
	}
	mustMine(t, fork, b)
	mustMine(t, fork, b)
	if _, err := left.Adopt(fork.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if left.Stats().PendingTxs != 1 {
		t.Fatal("orphaned transfer was lost")
	}
	mustMine(t, left, b)
	if account(t, left, b).Balance != 3*rewardAt(time.Now().Unix())+1 {
		t.Fatal("orphaned transfer not re-confirmed")
	}
}
func TestBadPeerCannotMintOrReset(t *testing.T) {
	a := testWallet(45)
	local := NewBlockchain()
	mustMine(t, local, a)
	before := local.Stats()
	remote := local.Snapshot()
	bad := NewBlock(2, remote.Blocks[1].Hash, nil, a.Address)
	bad.Reward = 1000 * UnitsPerGO
	bad.Mine(Difficulty)
	remote.Blocks = append(remote.Blocks, bad)
	if _, err := local.Adopt(remote); err == nil {
		t.Fatal("peer inflated supply")
	}
	if local.Stats() != before {
		t.Fatal("bad peer modified state")
	}
	remote = NewBlockchain().Snapshot()
	if adopted, err := local.Adopt(remote); adopted || err != nil {
		t.Fatal("short peer reset chain", err)
	}
	for _, u := range []string{"http://example.com", "file:///tmp/chain", "http://127.0.0.1:8080/evil"} {
		if _, err := newPeerManager(local, []string{u}); err == nil {
			t.Fatal("invalid peer accepted", u)
		}
	}
}
func TestConcurrentPeerAdoption(t *testing.T) {
	a := testWallet(46)
	first, second := NewBlockchain(), NewBlockchain()
	mustMine(t, first, a)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_, _ = second.Adopt(first.Snapshot())
				_ = second.Stats()
				_ = second.Blocks()
			}
		}()
	}
	mustMine(t, first, a)
	wg.Wait()
	if _, err := second.Adopt(first.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if first.Stats().TipHash != second.Stats().TipHash {
		t.Fatal("concurrent adoption failed")
	}
}
