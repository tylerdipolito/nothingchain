package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func signedCheckpoint(t *testing.T, s chainState, at int64, authority *Wallet) Checkpoint {
	t.Helper()
	tip := s.Blocks[len(s.Blocks)-1]
	c := Checkpoint{ChainID: ChainID, Height: tip.Index, Hash: tip.Hash, IssuedAt: at}
	key, err := authority.privateKey()
	if err != nil {
		t.Fatal(err)
	}
	c.Signature = hex.EncodeToString(ed25519.Sign(key, c.signingBytes()))
	return c
}

func TestCheckpointSignatureAndTrustBinding(t *testing.T) {
	a := testWallet(90)
	c := signedCheckpoint(t, NewBlockchain().Snapshot(), time.Now().Unix(), a)
	if err := c.verify(a.PublicKey); err != nil {
		t.Fatal(err)
	}
	if err := c.verify(testWallet(91).PublicKey); err == nil {
		t.Fatal("untrusted signer accepted")
	}
	for _, change := range []func(*Checkpoint){
		func(c *Checkpoint) { c.ChainID = "another-chain" },
		func(c *Checkpoint) { c.Height++ },
		func(c *Checkpoint) { c.IssuedAt++ },
		func(c *Checkpoint) { c.Hash = strings.Repeat("0", 64) },
		func(c *Checkpoint) { c.Signature = "00" },
	} {
		bad := c
		change(&bad)
		if err := bad.verify(a.PublicKey); err == nil {
			t.Fatal("modified certificate accepted")
		}
	}
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	data, _ := json.Marshal(c)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCheckpoint(path, a.PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCheckpoint(path, ""); err == nil {
		t.Fatal("certificate supplied its own trust")
	}
}

func TestCheckpointRejectsBackdatingAcrossHalving(t *testing.T) {
	a := testWallet(92)
	cutoff := utc("2028-02-29T00:00:00Z")
	s := NewBlockchain().Snapshot()
	before := NewBlock(1, s.Blocks[0].Hash, nil, a.Address)
	before.Timestamp = cutoff - 1
	before.Reward = rewardAt(before.Timestamp)
	before.Mine(Difficulty)
	s.Blocks = append(s.Blocks, before)
	old := signedCheckpoint(t, s, cutoff-1, a)
	if err := old.check(s, cutoff, true); err == nil {
		t.Fatal("pre-halving checkpoint permitted new work after cutoff")
	}
	if err := old.check(s, cutoff+CheckpointMaxAge+1, false); err != nil {
		t.Fatal("expired checkpoint prevented historical audit", err)
	}
	fresh := signedCheckpoint(t, s, cutoff, a)
	backdated := NewBlock(2, before.Hash, nil, a.Address)
	backdated.Timestamp = cutoff - 1
	backdated.Reward = rewardAt(backdated.Timestamp)
	backdated.Mine(Difficulty)
	s.Blocks = append(s.Blocks, backdated)
	if _, err := validateStateAt(s, false, cutoff); err != nil {
		t.Fatal("reproduction must pass legacy rules", err)
	}
	if err := fresh.check(s, cutoff, true); err == nil {
		t.Fatal("fresh checkpoint allowed pre-cutoff reward extension")
	}
	backdated.Timestamp = cutoff
	backdated.Reward = rewardAt(cutoff)
	backdated.Mine(Difficulty)
	if err := fresh.check(s, cutoff, true); err != nil {
		t.Fatal(err)
	}
	l, err := validateStateAt(s, false, cutoff)
	if err != nil || l.supply != 15*UnitsPerGO {
		t.Fatal("wrong supply in protected history", err)
	}
	if err := fresh.check(s, cutoff+CheckpointMaxAge+1, true); err == nil {
		t.Fatal("stale authority permitted new work")
	}
	conflicting := copyState(s)
	conflicting.Blocks = conflicting.Blocks[:1]
	if err := fresh.check(conflicting, cutoff, true); err == nil {
		t.Fatal("checkpoint anchor removed")
	}
}

func TestCheckpointBlocksInvalidAdoptionWithoutMutation(t *testing.T) {
	w := testWallet(93)
	bc := NewBlockchain()
	mustMine(t, bc, w)
	c := signedCheckpoint(t, bc.Snapshot(), time.Now().Unix(), w)
	bc.checkpoint = &c
	fork := NewBlockchain()
	mustMine(t, fork, testWallet(94))
	mustMine(t, fork, w)
	before, _ := json.Marshal(bc.Snapshot())
	if _, err := bc.Adopt(fork.Snapshot()); err == nil {
		t.Fatal("longer conflicting fork accepted")
	}
	after, _ := json.Marshal(bc.Snapshot())
	if string(before) != string(after) {
		t.Fatal("checkpoint rejection mutated state")
	}
	mustMine(t, bc, w)
	if !bc.Stats().CheckpointReady {
		t.Fatal("fresh valid checkpoint not ready")
	}
	c.IssuedAt = time.Now().Unix() - CheckpointMaxAge - 1
	if _, err := bc.MinePending(w.Address); err == nil {
		t.Fatal("mined with stale checkpoint")
	}
	if err := bc.AddTx(signed(t, w, testWallet(95).Address, 1, 1)); err == nil {
		t.Fatal("queued with stale checkpoint")
	}
}

func TestCheckpointBuildFailsBeforeCreatingUnprotectedLedger(t *testing.T) {
	if !CheckpointRequired {
		t.Skip("checkpoint build only")
	}
	path := filepath.Join(t.TempDir(), "never-created.json")
	if err := run([]string{"serve", "--data", path, "--miner", testWallet(96).Address}); err == nil {
		t.Fatal("unconfigured protected node started")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unconfigured startup created ledger")
	}
}
