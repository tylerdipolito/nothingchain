package main

import (
	"math"
	"testing"
	"time"
)

func utc(value string) int64 {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t.Unix()
}
func TestCalendarRewards(t *testing.T) {
	cases := []struct {
		when string
		want int64
	}{
		{"2028-02-28T23:59:59Z", 10 * UnitsPerGO},
		{"2028-02-29T00:00:00Z", 5 * UnitsPerGO},
		{"2028-03-01T00:00:00Z", 5 * UnitsPerGO},
		{"2032-02-28T23:59:59Z", 5 * UnitsPerGO},
		{"2032-02-29T00:00:00Z", 250_000_000},
		{"2036-02-29T00:00:00Z", 125_000_000},
		{"2100-03-01T00:00:00Z", InitialReward >> 18},
		{"2104-02-29T00:00:00Z", InitialReward >> 19},
		{"2300-03-01T00:00:00Z", 0},
	}
	for _, c := range cases {
		if got := rewardAt(utc(c.when)); got != c.want {
			t.Errorf("%s got %d want %d", c.when, got, c.want)
		}
	}
	if nextHalving(utc("2096-02-29T00:00:00Z")) != "2104-02-29T00:00:00Z" {
		t.Fatal("invented a leap day in 2100")
	}
}
func TestDecimalAmounts(t *testing.T) {
	for _, s := range []string{"0", "10", "2.5", "0.00000001", "92233720368.54775807"} {
		atoms, err := parseAmount(s)
		if err != nil {
			t.Fatal(s, err)
		}
		if formatAmount(atoms) != s {
			t.Fatal("precision lost", s, formatAmount(atoms))
		}
	}
	for _, s := range []string{"-1", "+1", "1e3", "NaN", "1.000000001", "92233720368.54775808", "92233720369", ".1", "1.", " 1", "1..2"} {
		if _, err := parseAmount(s); err == nil {
			t.Fatal("invalid amount accepted", s)
		}
	}
}
func TestHalvingConsensusAndTimestamp(t *testing.T) {
	a := testWallet(31)
	cutoff := utc("2028-02-29T00:00:00Z")
	bc := NewBlockchain()
	s := bc.Snapshot()
	before := NewBlock(1, s.Blocks[0].Hash, nil, a.Address)
	before.Timestamp = cutoff - 1
	before.Reward = rewardAt(before.Timestamp)
	before.Mine(Difficulty)
	after := NewBlock(2, before.Hash, nil, a.Address)
	after.Timestamp = cutoff
	after.Reward = rewardAt(after.Timestamp)
	after.Mine(Difficulty)
	s.Blocks = append(s.Blocks, before, after)
	l, err := validateStateAt(s, true, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if l.supply != 15*UnitsPerGO {
		t.Fatal("wrong supply across halving")
	}
	after.Reward = InitialReward
	after.Mine(Difficulty)
	if _, err := validateStateAt(s, true, cutoff); err == nil {
		t.Fatal("pre-halving reward accepted after cutoff")
	}
	after.Reward = rewardAt(cutoff)
	after.Timestamp = cutoff - 2
	after.Mine(Difficulty)
	if _, err := validateStateAt(s, true, cutoff); err == nil {
		t.Fatal("backwards timestamp accepted")
	}
	after.Timestamp = cutoff + MaxFutureSeconds + 1
	after.Mine(Difficulty)
	if _, err := validateStateAt(s, true, cutoff); err == nil {
		t.Fatal("future timestamp accepted")
	}
}
func TestFailedApplyDoesNotCorruptLedger(t *testing.T) {
	a, b := testWallet(32), testWallet(33)
	l := newLedger()
	l.balances[a.Address] = 1
	l.transferred = math.MaxInt64
	if err := l.apply(signed(t, a, b.Address, 1, 1)); err == nil {
		t.Fatal("overflow accepted")
	}
	if l.balances[a.Address] != 1 || l.balances[b.Address] != 0 || l.nonces[a.Address] != 0 {
		t.Fatal("failed tx modified ledger")
	}
}

// Characterization of a legacy consensus limitation, not a security guarantee.
// A calendar cutoff cannot establish elapsed real time from miner timestamps.
func TestLegacyCalendarRewardTimestampGrinding(t *testing.T) {
	w := testWallet(34)
	cutoff := utc("2028-02-29T00:00:00Z")
	makeHistory := func(timestamps ...int64) chainState {
		s := NewBlockchain().Snapshot()
		for _, timestamp := range timestamps {
			b := NewBlock(len(s.Blocks), s.Blocks[len(s.Blocks)-1].Hash, nil, w.Address)
			b.Timestamp = timestamp
			b.Reward = rewardAt(timestamp)
			b.Mine(Difficulty)
			s.Blocks = append(s.Blocks, b)
		}
		return s
	}
	honest := makeHistory(cutoff-1, cutoff)
	backdated := makeHistory(cutoff-1, cutoff-1, cutoff-1)
	h, err := validateStateAt(honest, false, cutoff+3600)
	if err != nil {
		t.Fatal(err)
	}
	b, err := validateStateAt(backdated, false, cutoff+3600)
	if err != nil {
		t.Fatal(err)
	}
	if !betterChain(backdated.Blocks, honest.Blocks) || h.supply != 15*UnitsPerGO || b.supply != 30*UnitsPerGO {
		t.Fatal("legacy timestamp reproduction no longer matches consensus; reassess characterization")
	}
	t.Log("legacy fork choice prefers backdated 30 GO history over honest 15 GO history at the same validation time")
}
