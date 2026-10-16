package main

import (
	"os"
	"testing"
)

func TestPoolDatabasePPLNSDistribution(t *testing.T) {
	dbPath := "test_pool_pplns.db"
	_ = os.Remove(dbPath)
	defer os.Remove(dbPath)

	db, err := NewPoolDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer db.Close()

	// Register 3 miners
	w1, _ := db.RegisterOrUpdateWorker("go1_miner_alpha.worker1", "go1_miner_alpha", "bc1q_alpha", "worker1")
	w2, _ := db.RegisterOrUpdateWorker("go1_miner_beta.worker2", "go1_miner_beta", "bc1q_beta", "worker2")
	w3, _ := db.RegisterOrUpdateWorker("go1_miner_gamma.worker3", "go1_miner_gamma", "bc1q_gamma", "worker3")

	if w1.WorkerName != "worker1" || w2.WorkerName != "worker2" || w3.WorkerName != "worker3" {
		t.Fatalf("unexpected worker names")
	}

	// Submit shares with varying difficulty
	// Alpha: 10 shares @ diff 16 = 160 work
	for i := 0; i < 10; i++ {
		accepted, isDup, err := db.RecordShare(w1.WorkerID, "job1", "00000001", "00000001", string(rune(i)), 16.0, "hash_alpha", false, false)
		if !accepted || isDup || err != nil {
			t.Fatalf("failed to record share alpha %d: isDup=%v err=%v", i, isDup, err)
		}
	}

	// Beta: 20 shares @ diff 16 = 320 work
	for i := 0; i < 20; i++ {
		accepted, isDup, err := db.RecordShare(w2.WorkerID, "job1", "00000002", "00000001", string(rune(i)), 16.0, "hash_beta", false, false)
		if !accepted || isDup || err != nil {
			t.Fatalf("failed to record share beta %d: isDup=%v err=%v", i, isDup, err)
		}
	}

	// Gamma: 10 shares @ diff 32 = 320 work
	for i := 0; i < 10; i++ {
		accepted, isDup, err := db.RecordShare(w3.WorkerID, "job1", "00000003", "00000001", string(rune(i)), 32.0, "hash_gamma", false, false)
		if !accepted || isDup || err != nil {
			t.Fatalf("failed to record share gamma %d: isDup=%v err=%v", i, isDup, err)
		}
	}

	// Total Work = 160 (Alpha: 20%) + 320 (Beta: 40%) + 320 (Gamma: 40%) = 800 work
	// Block found by Alpha!
	// Reward = 10 GO = 1,000,000,000 atoms
	// Hybrid Payout:
	// Finder Bonus (10%) = 100,000,000 atoms -> Alpha
	// Shared Pool (90%) = 900,000,000 atoms:
	// Alpha (20% of 900M) = 180,000,000 atoms -> Total Alpha = 100M + 180M = 280,000,000 atoms (2.80 GO)
	// Beta (40% of 900M)  = 360,000,000 atoms -> Total Beta  = 360,000,000 atoms (3.60 GO)
	// Gamma (40% of 900M) = 360,000,000 atoms -> Total Gamma = 360,000,000 atoms (3.60 GO)
	// Total Credited = 280M + 360M + 360M = 1,000,000,000 atoms EXACTLY.

	dist, err := db.DistributeGoBlockReward(101, "000000000000gochainblockhash101", w1.WorkerID, 1_000_000_000, PayoutModeHybrid, 1000)
	if err != nil {
		t.Fatalf("failed to distribute reward: %v", err)
	}

	if dist.FinderReward < 100_000_000 {
		t.Fatalf("finder reward too low: %d", dist.FinderReward)
	}

	workers, err := db.GetAllWorkers()
	if err != nil {
		t.Fatalf("failed to get workers: %v", err)
	}

	var alphaBal, betaBal, gammaBal int64
	for _, w := range workers {
		if w.WorkerID == w1.WorkerID {
			alphaBal = w.EarnedGOAtoms
		} else if w.WorkerID == w2.WorkerID {
			betaBal = w.EarnedGOAtoms
		} else if w.WorkerID == w3.WorkerID {
			gammaBal = w.EarnedGOAtoms
		}
	}

	t.Logf("💰 Balances after Block #101 (Hybrid PPLNS):")
	t.Logf("   Alpha (Finder + 20%% share): %d atoms (%.8f GO)", alphaBal, float64(alphaBal)/1e8)
	t.Logf("   Beta  (40%% share):         %d atoms (%.8f GO)", betaBal, float64(betaBal)/1e8)
	t.Logf("   Gamma (40%% share):         %d atoms (%.8f GO)", gammaBal, float64(gammaBal)/1e8)

	totalCredited := alphaBal + betaBal + gammaBal
	if totalCredited != 1_000_000_000 {
		t.Fatalf("conservation invariant broken: total credited %d != 1,000,000,000", totalCredited)
	}
	if alphaBal != 280_000_000 {
		t.Fatalf("expected alpha balance 280,000,000, got %d", alphaBal)
	}
	if betaBal != 360_000_000 {
		t.Fatalf("expected beta balance 360,000,000, got %d", betaBal)
	}
	if gammaBal != 360_000_000 {
		t.Fatalf("expected gamma balance 360,000,000, got %d", gammaBal)
	}

	// Duplicate Share Protection Test
	accepted, isDup, _ := db.RecordShare(w1.WorkerID, "job1", "00000001", "00000001", string(rune(0)), 16.0, "hash_dup", false, false)
	if accepted || !isDup {
		t.Fatalf("duplicate share was not rejected! accepted=%v, isDup=%v", accepted, isDup)
	}
	t.Logf("✅ Duplicate share correctly rejected by SQLite unique constraint!")
}

func TestPoolDatabaseRestartPersistence(t *testing.T) {
	dbPath := "test_pool_restart.db"
	_ = os.Remove(dbPath)
	defer os.Remove(dbPath)

	// Step 1: Open, insert data, close
	db1, err := NewPoolDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to open db1: %v", err)
	}
	w1, _ := db1.RegisterOrUpdateWorker("go1_miner_bitaxe.bitaxe01", "go1_miner_bitaxe", "bc1q_bitaxe", "bitaxe01")
	_, _, _ = db1.RecordShare(w1.WorkerID, "jobX", "00000001", "00000002", "00000003", 16.0, "hashX", false, false)
	_, _ = db1.DistributeGoBlockReward(200, "hash_block_200", w1.WorkerID, 1_000_000_000, PayoutModeSolo, 1000)
	db1.Close()

	// Step 2: Simulate complete coordinator crash & restart
	db2, err := NewPoolDatabase(dbPath)
	if err != nil {
		t.Fatalf("failed to open db2 after restart: %v", err)
	}
	defer db2.Close()

	workers, err := db2.GetAllWorkers()
	if err != nil || len(workers) != 1 {
		t.Fatalf("expected 1 worker after restart, got %d (err: %v)", len(workers), err)
	}

	w := workers[0]
	if w.WorkerID != "go1_miner_bitaxe.bitaxe01" || w.SharesAccepted != 1 || w.EarnedGOAtoms != 1_000_000_000 {
		t.Fatalf("worker state corrupted after restart: %+v", w)
	}

	t.Logf("✅ Verified 100%% restart persistence: Worker=%s, Shares=%d, Balance=%d atoms",
		w.WorkerID, w.SharesAccepted, w.EarnedGOAtoms)
}
