package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strings"
)

// AMM operations are carried in signed memos on the isolated AMM test chain.
// Their enclosing transfer must be a zero-amount self-transfer.
const faucetAtoms int64 = 10000 * UnitsPerGO
const maxTestSupply int64 = 100000000 * UnitsPerGO
const lockedShares int64 = 1000

type ammState struct {
	USD         map[string]int64 `json:"testusd_atoms"`
	Shares      map[string]int64 `json:"lp_shares_atoms"`
	Claimed     map[string]bool  `json:"faucet_claimed"`
	GOReserve   int64            `json:"go_reserve_atoms"`
	USDReserve  int64            `json:"testusd_reserve_atoms"`
	TotalShares int64            `json:"total_lp_shares_atoms"`
	Supply      int64            `json:"testusd_supply_atoms"`
}
type ammOperation struct {
	Kind     string `json:"kind"`
	Asset    string `json:"asset,omitempty"`
	A        int64  `json:"a,omitempty"`
	B        int64  `json:"b,omitempty"`
	MinA     int64  `json:"min_a,omitempty"`
	MinB     int64  `json:"min_b,omitempty"`
	Deadline int64  `json:"deadline_height"`
}

func newAMMState() *ammState {
	return &ammState{USD: map[string]int64{}, Shares: map[string]int64{}, Claimed: map[string]bool{}}
}
func mulDiv(a, b, c int64) int64 {
	if c <= 0 {
		return -1
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	n.Quo(n, big.NewInt(c))
	if !n.IsInt64() {
		return -1
	}
	return n.Int64()
}
func swapOutput(input, reserveIn, reserveOut int64) int64 {
	if input <= 0 || reserveIn <= 0 || reserveOut <= 0 {
		return 0
	}
	effective := new(big.Int).Mul(big.NewInt(input), big.NewInt(997))
	denominator := new(big.Int).Add(new(big.Int).Mul(big.NewInt(reserveIn), big.NewInt(1000)), effective)
	out := new(big.Int).Quo(new(big.Int).Mul(effective, big.NewInt(reserveOut)), denominator)
	if !out.IsInt64() {
		return 0
	}
	return out.Int64()
}
func (l *ledger) applyAMM(tx Transaction) error {
	if !AMMEnabled {
		return fmt.Errorf("AMM requires isolated test chain")
	}
	if tx.Amount != 0 || tx.To != tx.From {
		return fmt.Errorf("AMM requires zero-amount self-transfer")
	}
	var op ammOperation
	dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(tx.Memo, "amm:")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&op); err != nil {
		return err
	}
	canonical, _ := json.Marshal(op)
	if "amm:"+string(canonical) != tx.Memo {
		return fmt.Errorf("noncanonical AMM operation")
	}
	if op.Deadline < l.height || op.Deadline <= 0 {
		return fmt.Errorf("operation expired")
	}
	if op.A < 0 || op.B < 0 || op.MinA < 0 || op.MinB < 0 {
		return fmt.Errorf("negative amount")
	}
	p := l.amm
	who := tx.From
	switch op.Kind {
	case "faucet":
		if op.Asset != "" || op.A != 0 || op.B != 0 || op.MinA != 0 || op.MinB != 0 {
			return fmt.Errorf("invalid faucet fields")
		}
		if p.Claimed[who] || p.Supply > maxTestSupply-faucetAtoms {
			return fmt.Errorf("faucet already claimed or exhausted")
		}
		p.USD[who] += faucetAtoms
		p.Supply += faucetAtoms
		p.Claimed[who] = true
	case "add":
		if op.Asset != "" || op.MinB != 0 || op.A <= 0 || op.B <= 0 || op.MinA <= 0 {
			return fmt.Errorf("positive deposits and minimum shares required")
		}
		a, b := op.A, op.B
		var shares int64
		if p.TotalShares == 0 {
			root := new(big.Int).Sqrt(new(big.Int).Mul(big.NewInt(a), big.NewInt(b)))
			if !root.IsInt64() {
				return fmt.Errorf("share overflow")
			}
			shares = root.Int64() - lockedShares
		} else {
			shares = mulDiv(a, p.TotalShares, p.GOReserve)
			other := mulDiv(b, p.TotalShares, p.USDReserve)
			if other < shares {
				shares = other
			}
			// Round deposits up, so new providers cannot dilute existing LPs.
			a = ceilMulDiv(shares, p.GOReserve, p.TotalShares)
			b = ceilMulDiv(shares, p.USDReserve, p.TotalShares)
		}
		extra := int64(0)
		if p.TotalShares == 0 {
			extra = lockedShares
		}
		if shares <= 0 || shares < op.MinA || a <= 0 || b <= 0 || a > op.A || b > op.B || l.balances[who] < a || p.USD[who] < b || p.TotalShares > math.MaxInt64-extra-shares || p.GOReserve > math.MaxInt64-a || p.USDReserve > math.MaxInt64-b {
			return fmt.Errorf("insufficient deposit, minimum shares, or overflow")
		}
		l.balances[who] -= a
		p.USD[who] -= b
		p.GOReserve += a
		p.USDReserve += b
		p.TotalShares += shares + extra
		p.Shares[who] += shares
	case "swap":
		if op.A <= 0 || op.MinA <= 0 || op.B != 0 || op.MinB != 0 {
			return fmt.Errorf("positive input and minimum output required")
		}
		switch op.Asset {
		case "GO":
			out := swapOutput(op.A, p.GOReserve, p.USDReserve)
			if out <= 0 || out < op.MinA || l.balances[who] < op.A || p.GOReserve > math.MaxInt64-op.A || p.USD[who] > math.MaxInt64-out {
				return fmt.Errorf("swap balance, slippage, or overflow")
			}
			l.balances[who] -= op.A
			p.GOReserve += op.A
			p.USDReserve -= out
			p.USD[who] += out
		case "testUSD":
			out := swapOutput(op.A, p.USDReserve, p.GOReserve)
			if out <= 0 || out < op.MinA || p.USD[who] < op.A || p.USDReserve > math.MaxInt64-op.A || l.balances[who] > math.MaxInt64-out {
				return fmt.Errorf("swap balance, slippage, or overflow")
			}
			p.USD[who] -= op.A
			p.USDReserve += op.A
			p.GOReserve -= out
			l.balances[who] += out
		default:
			return fmt.Errorf("asset must be GO or testUSD")
		}
	case "remove":
		if op.Asset != "" || op.A <= 0 || op.B != 0 || op.MinA <= 0 || op.MinB <= 0 || p.Shares[who] < op.A {
			return fmt.Errorf("invalid shares or minimum withdrawals")
		}
		a, b := mulDiv(op.A, p.GOReserve, p.TotalShares), mulDiv(op.A, p.USDReserve, p.TotalShares)
		if a <= 0 || b <= 0 || a < op.MinA || b < op.MinB || l.balances[who] > math.MaxInt64-a || p.USD[who] > math.MaxInt64-b {
			return fmt.Errorf("withdrawal slippage or overflow")
		}
		p.Shares[who] -= op.A
		p.TotalShares -= op.A
		p.GOReserve -= a
		p.USDReserve -= b
		l.balances[who] += a
		p.USD[who] += b
	default:
		return fmt.Errorf("unknown AMM operation")
	}
	return nil
}
func ceilMulDiv(a, b, c int64) int64 {
	if a <= 0 || b <= 0 || c <= 0 {
		return -1
	}
	n := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	n.Add(n, big.NewInt(c-1))
	n.Quo(n, big.NewInt(c))
	if !n.IsInt64() {
		return -1
	}
	return n.Int64()
}
func (bc *Blockchain) ammSnapshot() (*ammState, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	l, e := validateState(bc.state, false)
	if e != nil {
		return nil, e
	}
	return l.amm, nil
}
func registerAMM(mux *http.ServeMux, bc *Blockchain) {
	if !AMMEnabled {
		return
	}
	mux.HandleFunc("GET /amm", func(w http.ResponseWriter, r *http.Request) {
		p, e := bc.ammSnapshot()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"network": ChainID, "warning": "testUSD is unbacked test money; no dollar redemption", "fee_bps": 30, "go_reserve": formatAmount(p.GOReserve), "testusd_reserve": formatAmount(p.USDReserve), "state": p})
	})
}
func runAMM(args []string) error {
	if !AMMEnabled {
		return fmt.Errorf("build with -tags amm for isolated AMM testnet")
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: amm status|quote|faucet|add|swap|remove [flags]")
	}
	fs := flags("amm")
	node := fs.String("node", DefaultNode, "node URL")
	wallet := fs.String("wallet", WalletFile, "wallet file")
	asset := fs.String("asset", "", "swap input: GO or testUSD")
	a := fs.String("a", "0", "input GO / swap input / LP shares")
	b := fs.String("b", "0", "maximum testUSD deposit")
	minA := fs.String("min-a", "0", "minimum shares / output / withdrawn GO")
	minB := fs.String("min-b", "0", "minimum withdrawn testUSD")
	ttl := fs.Int64("ttl", 20, "valid for this many blocks")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "status" {
		var out any
		if e := request(*node, "/amm", nil, &out); e != nil {
			return e
		}
		return printJSON(out)
	}
	op := ammOperation{Kind: args[0], Asset: *asset}
	var err error
	for _, v := range []struct {
		s string
		p *int64
	}{{*a, &op.A}, {*b, &op.B}, {*minA, &op.MinA}, {*minB, &op.MinB}} {
		*v.p, err = parseAmount(v.s)
		if err != nil {
			return err
		}
	}
	if args[0] == "quote" {
		var out struct {
			State ammState `json:"state"`
		}
		if e := request(*node, "/amm", nil, &out); e != nil {
			return e
		}
		p := out.State
		var result int64
		if *asset == "GO" {
			result = swapOutput(op.A, p.GOReserve, p.USDReserve)
		} else if *asset == "testUSD" {
			result = swapOutput(op.A, p.USDReserve, p.GOReserve)
		} else {
			return fmt.Errorf("asset must be GO or testUSD")
		}
		return printJSON(map[string]any{"output": formatAmount(result), "fee_bps": 30, "note": "quote only; use --min-a to limit execution slippage"})
	}
	w, err := LoadWallet(*wallet)
	if err != nil {
		return err
	}
	var acct Account
	var stats Stats
	if err = request(*node, "/stats", nil, &stats); err != nil {
		return err
	}
	if stats.ChainID != ChainID {
		return fmt.Errorf("wrong network")
	}
	if *ttl <= 0 || *ttl > 1000 {
		return fmt.Errorf("ttl must be 1..1000 blocks")
	}
	op.Deadline = int64(stats.Height) + *ttl
	if err = request(*node, "/balance/"+w.Address, nil, &acct); err != nil {
		return err
	}
	data, _ := json.Marshal(op)
	tx, err := w.Sign(w.Address, 0, "amm:"+string(data), acct.NextNonce)
	if err != nil {
		return err
	}
	var result any
	if err = request(*node, "/tx", tx, &result); err != nil {
		return err
	}
	return printJSON(result)
}
