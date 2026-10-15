# Reviewer handoff

Repository: https://github.com/tylerdipolito/nothingchain
Branch: gochain-amm-review
Original baseline: abd9a86af75162e3dd3efc1e70d114e67261c576

Read README.md, SPEC.md, and AMM.md. Review the entire branch against the baseline,
then focus on simulation.go, amm_test.go, simulation_test.go, and scripts/ for the
latest work. Build and actually run both network variants using fresh test data.

```powershell
go test ./...
go test -tags amm ./...
go vet ./...
go vet -tags amm ./...
go test -race -tags amm ./...
python -m unittest discover -s scripts -p test_reward_liquidity.py -v
go build -tags amm -o gochain-sim.exe .
.\gochain-sim.exe simulate --seed 42 --attempts 100 --out simulation-local
```

Review supply conservation, LP ownership and dilution, overflow/rounding, signature
and nonce replay, slippage/expiry, mempool effects, fork adoption and orphan AMM
transactions. For the reward helper, test interrupted submissions, stale locks,
restart behavior, budget authorization, concurrent wallet use, and reorganizations.
For the simulator, verify independence from live ledgers, reproducibility, quote
versus execution checks, accounting and clearly stated limitations.

Known limits: fixed PoW difficulty and local one-minute timer, loopback-only peers,
timestamp-dependent calendar issuance, unencrypted wallet seeds, unbacked testUSD,
public deterministic simulator keys, single-threaded simulated trading, and no
production audit. The reward helper submits one bounded deposit per journal;
multiple journals authorize separate budgets. It is not a continuous reinvestment
service. Dynamic fees, privacy and post-quantum cryptography are not implemented.

Report confirmed findings by severity with reproducible steps, impact, code
locations, and recommended fixes. Distinguish test/model limitations from security
bugs. Do not change economics, deploy, or use real assets during the review.
