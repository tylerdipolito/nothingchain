//go:build amm

package main

import (
	"encoding/json"
	"math"
	"math/big"
	"path/filepath"
	"testing"
)

func ammTx(t *testing.T, bc *Blockchain, w *Wallet, op ammOperation) Transaction {
	t.Helper()
	a, e := bc.Account(w.Address)
	if e != nil {
		t.Fatal(e)
	}
	if op.Deadline == 0 {
		op.Deadline = 1000
	}
	data, _ := json.Marshal(op)
	tx, e := w.Sign(w.Address, 0, "amm:"+string(data), a.NextNonce)
	if e != nil {
		t.Fatal(e)
	}
	return tx
}
func applyOp(t *testing.T, bc *Blockchain, w *Wallet, op ammOperation) {
	t.Helper()
	if e := bc.AddTx(ammTx(t, bc, w, op)); e != nil {
		t.Fatal(e)
	}
	mustMine(t, bc, w)
	checkConservation(t, bc)
}
func checkConservation(t *testing.T, bc *Blockchain) {
	t.Helper()
	l, e := validateState(bc.state, false)
	if e != nil {
		t.Fatal(e)
	}
	goTotal, usdTotal, shares := l.amm.GOReserve, l.amm.USDReserve, int64(0)
	for _, b := range l.balances {
		if b < 0 {
			t.Fatal("negative GO")
		}
		goTotal += b
	}
	for _, b := range l.amm.USD {
		if b < 0 {
			t.Fatal("negative USD")
		}
		usdTotal += b
	}
	for _, b := range l.amm.Shares {
		shares += b
	}
	if l.amm.TotalShares > 0 {
		shares += lockedShares
	}
	if goTotal != l.supply || usdTotal != l.amm.Supply || shares != l.amm.TotalShares {
		t.Fatal("conservation violated")
	}
}
func TestAMMFullLifecycleRestartAndRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amm.json")
	bc, e := LoadOrCreate(path)
	if e != nil {
		t.Fatal(e)
	}
	w := testWallet(51)
	other := testWallet(52)
	for i := 0; i < 3; i++ {
		mustMine(t, bc, w)
	}
	applyOp(t, bc, w, ammOperation{Kind: "faucet"})
	if e := bc.AddTx(ammTx(t, bc, w, ammOperation{Kind: "faucet"})); e == nil {
		t.Fatal("double faucet")
	}
	applyOp(t, bc, w, ammOperation{Kind: "add", A: 20 * UnitsPerGO, B: 2000 * UnitsPerGO, MinA: 1})
	before, _ := bc.ammSnapshot()
	kBefore := new(big.Int).Mul(big.NewInt(before.GOReserve), big.NewInt(before.USDReserve))
	quote := swapOutput(100*UnitsPerGO, before.USDReserve, before.GOReserve)
	bad := ammTx(t, bc, w, ammOperation{Kind: "swap", Asset: "testUSD", A: 100 * UnitsPerGO, MinA: quote + 1})
	if e := bc.AddTx(bad); e == nil {
		t.Fatal("slippage bypass")
	}
	applyOp(t, bc, w, ammOperation{Kind: "swap", Asset: "testUSD", A: 100 * UnitsPerGO, MinA: quote})
	after, _ := bc.ammSnapshot()
	kAfter := new(big.Int).Mul(big.NewInt(after.GOReserve), big.NewInt(after.USDReserve))
	if kAfter.Cmp(kBefore) < 0 {
		t.Fatal("product shrank")
	}
	applyOp(t, bc, w, ammOperation{Kind: "swap", Asset: "GO", A: UnitsPerGO, MinA: 1})
	applyOp(t, bc, w, ammOperation{Kind: "add", A: UnitsPerGO, B: 500 * UnitsPerGO, MinA: 1})
	if e := bc.AddTx(ammTx(t, bc, other, ammOperation{Kind: "remove", A: 1, MinA: 1, MinB: 1})); e == nil {
		t.Fatal("stolen shares")
	}
	p, _ := bc.ammSnapshot()
	applyOp(t, bc, w, ammOperation{Kind: "remove", A: p.Shares[w.Address], MinA: 1, MinB: 1})
	p, _ = bc.ammSnapshot()
	if p.TotalShares != lockedShares || p.Shares[w.Address] != 0 {
		t.Fatal("withdrawal accounting")
	}
	restored, e := LoadOrCreate(path)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(p)
	rp, _ := restored.ammSnapshot()
	b, _ := json.Marshal(rp)
	if string(a) != string(b) {
		t.Fatal("restart changed pool")
	}
	expired := ammTx(t, bc, w, ammOperation{Kind: "faucet", Deadline: 1})
	if e := bc.AddTx(expired); e == nil {
		t.Fatal("expired accepted")
	}
	forged := ammTx(t, bc, other, ammOperation{Kind: "faucet"})
	forged.Memo = "amm:{}"
	if e := bc.AddTx(forged); e == nil {
		t.Fatal("forgery accepted")
	}
}
func TestAMMArithmeticBounds(t *testing.T) {
	for _, n := range []int64{1, 100, UnitsPerGO, math.MaxInt64} {
		out := swapOutput(n, math.MaxInt64, math.MaxInt64)
		if out < 0 || out >= math.MaxInt64 {
			t.Fatal("bad quote")
		}
	}
	if mulDiv(math.MaxInt64, math.MaxInt64, 1) != -1 {
		t.Fatal("overflow accepted")
	}
	if ceilMulDiv(1, 1, 3) != 1 {
		t.Fatal("bad ceiling")
	}
}
