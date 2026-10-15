package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
)

type simulationTrade struct {
	Attempt   int    `json:"attempt"`
	Trader    int    `json:"trader"`
	Asset     string `json:"input_asset"`
	Input     int64  `json:"input_atoms"`
	Output    int64  `json:"output_atoms"`
	Reference int64  `json:"reference_testusd_per_go_atoms"`
	Outcome   string `json:"outcome"`
}

// Deterministic keys are deliberately public test fixtures. Never fund them externally.
func simulationWallet(seed int64, index int) *Wallet {
	digest := sha256.Sum256([]byte(fmt.Sprintf("PUBLIC-UNSAFE-SIMULATION-KEY:%d:%d", seed, index)))
	key := ed25519.NewKeyFromSeed(digest[:])
	pub := key.Public().(ed25519.PublicKey)
	return &Wallet{Address: addressFor(pub), PublicKey: hex.EncodeToString(pub), Seed: hex.EncodeToString(digest[:])}
}
func simulateTrades(seed int64, attempts int, path string) (map[string]any, error) {
	bc, e := LoadOrCreate(path)
	if e != nil {
		return nil, e
	}
	if bc.Stats().Height != 0 {
		return nil, fmt.Errorf("simulation requires a fresh ledger")
	}
	miner, lp := simulationWallet(seed, 7), simulationWallet(seed, 6)
	mine := func(w *Wallet) error {
		// A fixed historical clock makes hashes and rewards repeatable across runs.
		// Only this offline harness uses it; live mining keeps its ordinary clock.
		bc.mu.Lock()
		defer bc.mu.Unlock()
		state := copyState(bc.state)
		b := NewBlock(len(state.Blocks), state.Blocks[len(state.Blocks)-1].Hash, state.Pending, w.Address)
		b.Timestamp = 1728000000 + int64(b.Index)
		b.Reward = rewardAt(b.Timestamp)
		b.Mine(Difficulty)
		state.Blocks = append(state.Blocks, b)
		state.Pending = []Transaction{}
		if _, err := validateState(state, true); err != nil {
			return err
		}
		if err := bc.persist(state); err != nil {
			return err
		}
		bc.state = state
		return nil
	}
	submit := func(w *Wallet, op ammOperation) error {
		acct, e := bc.Account(w.Address)
		if e != nil {
			return e
		}
		op.Deadline = int64(bc.Stats().Height) + 20
		memo, _ := json.Marshal(op)
		tx, e := w.Sign(w.Address, 0, "amm:"+string(memo), acct.NextNonce)
		if e != nil {
			return e
		}
		return bc.AddTx(tx)
	}
	wallets := make([]*Wallet, 6)
	for i := range wallets {
		w := simulationWallet(seed, i)
		wallets[i] = w
		for j := 0; j < 5; j++ {
			if e := mine(w); e != nil {
				return nil, e
			}
		}
		if e := submit(w, ammOperation{Kind: "faucet"}); e != nil {
			return nil, e
		}
		if e := mine(miner); e != nil {
			return nil, e
		}
	}
	for j := 0; j < 10; j++ {
		if e := mine(lp); e != nil {
			return nil, e
		}
	}
	if e := submit(lp, ammOperation{Kind: "faucet"}); e != nil {
		return nil, e
	}
	if e := mine(miner); e != nil {
		return nil, e
	}
	if e := submit(lp, ammOperation{Kind: "add", A: 100 * UnitsPerGO, B: 10000 * UnitsPerGO, MinA: 1}); e != nil {
		return nil, e
	}
	if e := mine(miner); e != nil {
		return nil, e
	}
	rng := rand.New(rand.NewSource(seed))
	reference := int64(100 * UnitsPerGO)
	records := make([]simulationTrade, 0, attempts)
	executed, rejected, skipped := 0, 0, 0
	feesGO, feesUSD, volumeUSD := int64(0), int64(0), int64(0)
	for i := 0; i < attempts; i++ {
		reference = reference * int64(9800+rng.Intn(401)) / 10000
		if reference < 50*UnitsPerGO {
			reference = 50 * UnitsPerGO
		}
		if reference > 200*UnitsPerGO {
			reference = 200 * UnitsPerGO
		}
		trader := i % 6
		asset := "testUSD"
		input := int64(25+rng.Intn(176)) * UnitsPerGO
		if trader == 2 || trader == 3 || (trader == 4 && rng.Intn(2) == 0) {
			asset = "GO"
			input = int64(1+rng.Intn(3)) * UnitsPerGO
		}
		p, _ := bc.ammSnapshot()
		if trader == 5 { // Small, price-sensitive arbitrage against an external simulated venue.
			if mulDiv(p.USDReserve, UnitsPerGO, p.GOReserve) > reference {
				asset = "GO"
				input = UnitsPerGO
			} else {
				asset = "testUSD"
				input = 100 * UnitsPerGO
			}
		}
		inReserve, outReserve := p.USDReserve, p.GOReserve
		if asset == "GO" {
			inReserve, outReserve = p.GOReserve, p.USDReserve
		}
		output := swapOutput(input, inReserve, outReserve)
		record := simulationTrade{Attempt: i + 1, Trader: trader, Asset: asset, Input: input, Output: output, Reference: reference}
		if trader == 5 && ((asset == "GO" && output <= mulDiv(input, reference, UnitsPerGO)) || (asset == "testUSD" && mulDiv(output, reference, UnitsPerGO) <= input)) {
			record.Outcome = "skipped_unprofitable"
			skipped++
			records = append(records, record)
			continue
		}
		minimum := output * 99 / 100
		if minimum < 1 {
			minimum = 1
		}
		if i%17 == 0 {
			minimum = output + 1
		}
		if e := submit(wallets[trader], ammOperation{Kind: "swap", Asset: asset, A: input, MinA: minimum}); e != nil {
			record.Outcome = "rejected: " + e.Error()
			rejected++
		} else {
			if e := mine(miner); e != nil {
				return nil, e
			}
			after, _ := bc.ammSnapshot()
			if asset == "GO" {
				if p.USDReserve-after.USDReserve != output || after.GOReserve-p.GOReserve != input {
					return nil, fmt.Errorf("execution differs from quote")
				}
				feesGO += input * 3 / 1000
				volumeUSD += mulDiv(input, reference, UnitsPerGO)
			} else {
				if p.GOReserve-after.GOReserve != output || after.USDReserve-p.USDReserve != input {
					return nil, fmt.Errorf("execution differs from quote")
				}
				feesUSD += input * 3 / 1000
				volumeUSD += input
			}
			record.Outcome = "executed"
			executed++
		}
		if e := bc.Validate(); e != nil {
			return nil, e
		}
		records = append(records, record)
	}
	l, err := validateState(bc.state, false)
	if err != nil {
		return nil, err
	}
	goSum, usdSum := l.amm.GOReserve, l.amm.USDReserve
	for _, amount := range l.balances {
		goSum += amount
	}
	for _, amount := range l.amm.USD {
		usdSum += amount
	}
	if goSum != l.supply || usdSum != l.amm.Supply {
		return nil, fmt.Errorf("simulation conservation failure")
	}
	p, _ := bc.ammSnapshot()
	share := p.Shares[lp.Address]
	lpGO, lpUSD := mulDiv(share, p.GOReserve, p.TotalShares), mulDiv(share, p.USDReserve, p.TotalShares)
	value := lpUSD + mulDiv(lpGO, reference, UnitsPerGO)
	hold := 10000*UnitsPerGO + 100*reference
	balances := make([]map[string]any, 6)
	for i, w := range wallets {
		a, _ := bc.Account(w.Address)
		balances[i] = map[string]any{"trader": i, "go": a.BalanceGO, "testusd": formatAmount(p.USD[w.Address]), "end_value_testusd": formatAmount(p.USD[w.Address] + mulDiv(a.Balance, reference, UnitsPerGO)), "hold_value_testusd": formatAmount(10000*UnitsPerGO + 50*reference)}
	}
	return map[string]any{"seed": seed, "attempts": attempts, "executed": executed, "rejected": rejected, "skipped": skipped, "fee_bps": 30, "final_reference": formatAmount(reference), "go_reserve": formatAmount(p.GOReserve), "testusd_reserve": formatAmount(p.USDReserve), "lp_value_testusd": formatAmount(value), "hold_value_testusd": formatAmount(hold), "lp_minus_hold_atoms": value - hold, "fee_input_go": formatAmount(feesGO), "fee_input_testusd": formatAmount(feesUSD), "reference_marked_input_volume_testusd": formatAmount(volumeUSD), "wallets": balances, "trades": records, "valid": bc.Stats().Valid, "limitations": "Artificial reference prices and bounded trader policies, no gas or external venue costs; not a forecast. LP value includes swap fees and locked-share dilution; not pure impermanent loss. Mining rewards go to a separate account. Public deterministic keys must never receive real assets."}, nil
}
func runSimulation(args []string) error {
	if !AMMEnabled {
		return fmt.Errorf("simulation requires -tags amm")
	}
	fs := flags("simulate")
	seed := fs.Int64("seed", 42, "repeatable scenario seed")
	attempts := fs.Int("attempts", 100, "trade attempts, 1..1000")
	out := fs.String("out", "simulation-local", "fresh output directory")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if *attempts < 1 || *attempts > 1000 {
		return fmt.Errorf("attempts must be 1..1000")
	}
	if e := os.Mkdir(*out, 0700); e != nil {
		return fmt.Errorf("output must not already exist: %w", e)
	}
	result, e := simulateTrades(*seed, *attempts, filepath.Join(*out, "ledger.json"))
	if e != nil {
		return e
	}
	data, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*out, "results.json"), data, 0600); e != nil {
		return e
	}
	delete(result, "trades")
	return printJSON(result)
}
