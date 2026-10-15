//go:build amm

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSimulationReproducible(t *testing.T) {
	a, e := simulateTrades(42, 6, filepath.Join(t.TempDir(), "ledger.json"))
	if e != nil {
		t.Fatal(e)
	}
	b, e := simulateTrades(42, 6, filepath.Join(t.TempDir(), "ledger.json"))
	if e != nil {
		t.Fatal(e)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if string(x) != string(y) {
		t.Fatal("same seed produced different economic results")
	}
	if a["executed"].(int)+a["rejected"].(int)+a["skipped"].(int) != 6 {
		t.Fatal("missing attempts")
	}
	if a["rejected"].(int) == 0 {
		t.Fatal("slippage rejection not exercised")
	}
}
