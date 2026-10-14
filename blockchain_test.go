package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testWallet(n byte) *Wallet {
	seed := bytes.Repeat([]byte{n}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &Wallet{Address: addressFor(pub), PublicKey: hex.EncodeToString(pub), Seed: hex.EncodeToString(seed)}
}
func mustMine(t *testing.T, bc *Blockchain, w *Wallet) *Block {
	t.Helper()
	b, err := bc.MinePending(w.Address)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func signed(t *testing.T, w *Wallet, to string, amount int64, nonce uint64) Transaction {
	t.Helper()
	tx, err := w.Sign(to, amount, "test", nonce)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func account(t *testing.T, bc *Blockchain, w *Wallet) Account {
	t.Helper()
	a, err := bc.Account(w.Address)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMintTransferAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain.json")
	bc, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := testWallet(1), testWallet(2)
	r := rewardAt(time.Now().Unix())
	amount := r / 2
	if bc.Stats().TotalSupply != 0 {
		t.Fatal("premine")
	}
	mustMine(t, bc, a)
	mustMine(t, bc, a)
	tx := signed(t, a, b.Address, amount, 1)
	if err := bc.AddTx(tx); err != nil {
		t.Fatal(err)
	}
	if got := account(t, bc, a); got.Balance != 2*r || got.Available != 2*r-amount || got.NextNonce != 2 {
		t.Fatal(got)
	}
	if err := bc.AddTx(tx); err == nil {
		t.Fatal("replay accepted")
	}
	if err := bc.AddTx(signed(t, a, b.Address, 2*r, 2)); err == nil {
		t.Fatal("pending double spend accepted")
	}
	mustMine(t, bc, b)
	if account(t, bc, a).Balance != 2*r-amount || account(t, bc, b).Balance != r+amount {
		t.Fatal("wrong balances")
	}
	s := bc.Stats()
	if s.TotalSupply != 3*r || s.TotalTransferred != amount || s.PendingTxs != 0 || !s.Valid {
		t.Fatal(s)
	}
	restarted, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if account(t, restarted, b).Balance != r+amount || restarted.Stats().TotalSupply != 3*r {
		t.Fatal("restart lost balances")
	}
	if err := restarted.AddTx(tx); err == nil {
		t.Fatal("replay after restart accepted")
	}
}
func TestSignaturesAndSelfTransfers(t *testing.T) {
	a, b := testWallet(3), testWallet(4)
	bc := NewBlockchain()
	mustMine(t, bc, a)
	tx := signed(t, a, b.Address, 1, 1)
	tx.Amount = 0
	if err := bc.AddTx(tx); err == nil {
		t.Fatal("tampered signature accepted")
	}
	tx = signed(t, b, a.Address, 0, 1)
	tx.From = a.Address
	if err := bc.AddTx(tx); err == nil {
		t.Fatal("impersonation accepted")
	}
	if _, err := a.Sign(b.Address, -1, "", 1); err == nil {
		t.Fatal("negative transfer")
	}
	if err := bc.AddTx(signed(t, a, b.Address, 1, 2)); err == nil {
		t.Fatal("skipped nonce accepted")
	}
	if err := bc.AddTx(signed(t, a, a.Address, 1, 1)); err != nil {
		t.Fatal(err)
	}
	mustMine(t, bc, b)
	if account(t, bc, a).Balance != rewardAt(time.Now().Unix()) || account(t, bc, b).Balance != rewardAt(time.Now().Unix()) || bc.Stats().TotalSupply != 2*rewardAt(time.Now().Unix()) {
		t.Fatal("self-transfer minted coins")
	}
	if _, err := bc.MinePending("me"); err == nil {
		t.Fatal("unowned alias accepted as wallet")
	}
}
func TestRejectForgedChain(t *testing.T) {
	a := testWallet(5)
	original := NewBlockchain()
	mustMine(t, original, a)
	cases := map[string]func(*chainState){
		"extra reward":  func(s *chainState) { s.Blocks[1].Reward = 2; s.Blocks[1].Mine(Difficulty) },
		"zero reward":   func(s *chainState) { s.Blocks[1].Reward = 0; s.Blocks[1].Mine(Difficulty) },
		"fake genesis":  func(s *chainState) { s.Blocks = s.Blocks[:1]; s.Blocks[0].Timestamp++; s.Blocks[0].Mine(Difficulty) },
		"bad index":     func(s *chainState) { s.Blocks[1].Index = 9; s.Blocks[1].Mine(Difficulty) },
		"bad link":      func(s *chainState) { s.Blocks[1].PrevHash = "wrong"; s.Blocks[1].Mine(Difficulty) },
		"time reversal": func(s *chainState) { s.Blocks[1].Timestamp = 0; s.Blocks[1].Mine(Difficulty) },
		"no work": func(s *chainState) {
			b := s.Blocks[1]
			b.Nonce = 0
			b.Hash = b.computeHash()
			for strings.HasPrefix(b.Hash, "0000") {
				b.Nonce++
				b.Hash = b.computeHash()
			}
		},
		"unsigned transfer": func(s *chainState) {
			s.Blocks[1].Txs = []Transaction{{From: a.Address, To: a.Address, Amount: 100}}
			s.Blocks[1].Mine(Difficulty)
		},
		"null block":    func(s *chainState) { s.Blocks[1] = nil },
		"empty chain":   func(s *chainState) { s.Blocks = nil },
		"wrong network": func(s *chainState) { s.ChainID = "nothingchain" },
		"bad pending":   func(s *chainState) { s.Pending = []Transaction{{From: "network", To: a.Address, Amount: 100}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := chainState{ChainID: ChainID, Blocks: original.Blocks(), Pending: []Transaction{}}
			mutate(&s)
			if _, err := validateState(s, true); err == nil {
				t.Fatal("invalid state accepted")
			}
			path := filepath.Join(t.TempDir(), "chain.json")
			data, _ := json.Marshal(s)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOrCreate(path); err == nil {
				t.Fatal("invalid ledger silently accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(data, after) {
				t.Fatal("invalid ledger replaced")
			}
		})
	}
}
func TestPersistenceFailureRollsBack(t *testing.T) {
	a := testWallet(6)
	bc := NewBlockchain()
	mustMine(t, bc, a)
	bc.path = filepath.Join(t.TempDir(), "missing", "chain.json")
	if err := bc.AddTx(signed(t, a, a.Address, 0, 1)); err == nil {
		t.Fatal("failed save reported success")
	}
	if bc.Stats().PendingTxs != 0 {
		t.Fatal("failed send changed memory")
	}
	if _, err := bc.MinePending(a.Address); err == nil {
		t.Fatal("failed mine save reported success")
	}
	if bc.Stats().TotalSupply != rewardAt(time.Now().Unix()) {
		t.Fatal("failed mine changed supply")
	}
}
func TestWalletFileAndWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet.json")
	w, err := CreateWallet(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWallet(path); err == nil {
		t.Fatal("overwrote wallet")
	}
	loaded, err := LoadWallet(path)
	if err != nil || loaded.Address != w.Address {
		t.Fatal("wallet round trip", err)
	}
	w.Address = testWallet(7).Address
	data, _ := json.Marshal(w)
	_ = os.WriteFile(path, data, 0600)
	if _, err := LoadWallet(path); err == nil {
		t.Fatal("corrupt wallet accepted")
	}
	chain := filepath.Join(t.TempDir(), "chain.json")
	release, err := acquireChainLock(chain)
	if err != nil {
		t.Fatal(err)
	}
	if unlock, err := acquireChainLock(chain); err == nil {
		unlock()
		t.Fatal("second writer admitted")
	}
	release()
	release, err = acquireChainLock(chain)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestHTTPAndConcurrentAccess(t *testing.T) {
	a, b := testWallet(8), testWallet(9)
	bc := NewBlockchain()
	mustMine(t, bc, a)
	server := httptest.NewServer(newHandler(bc, a.Address, false, time.Minute))
	defer server.Close()
	tx := signed(t, a, b.Address, 1, 1)
	var result any
	if err := request(server.URL, "/tx", tx, &result); err != nil {
		t.Fatal(err)
	}
	if err := request(server.URL, "/tx", tx, &result); err == nil {
		t.Fatal("HTTP replay accepted")
	}
	for _, body := range []string{`{"from":"network","to":"me","amount":100}`, `{"miner":"me"}`, `{} {}`, `null`} {
		endpoint := "/mine"
		if strings.Contains(body, "from") {
			endpoint = "/tx"
		}
		res, err := http.Post(server.URL+endpoint, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == 200 {
			t.Fatalf("accepted invalid body %s", body)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				_ = bc.Stats()
				_, _ = bc.Account(a.Address)
				_ = bc.Blocks()
			}
		}()
	}
	mustMine(t, bc, b)
	wg.Wait()
	if err := bc.Validate(); err != nil {
		t.Fatal(err)
	}
	blocks := bc.Blocks()
	blocks[1].Reward = 100
	if bc.Stats().TotalSupply != 2*rewardAt(time.Now().Unix()) {
		t.Fatal("snapshot mutation changed ledger")
	}
}
func TestAutomaticMiningStops(t *testing.T) {
	bc := NewBlockchain()
	a := testWallet(10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go mineLoop(ctx, bc, a.Address, time.Hour, done)
	deadline := time.Now().Add(10 * time.Second)
	for bc.Stats().Blocks < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("miner did not stop")
	}
	if bc.Stats().TotalSupply != rewardAt(time.Now().Unix()) {
		t.Fatal("automatic mining did not create exactly one reward")
	}
}
