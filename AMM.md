# GoChain AMM testnet

This is an isolated, experimental GO/testUSD constant-product AMM. testUSD is
unbacked faucet money, not a stablecoin, deposit, or redeemable dollar claim.
Existing GO balances on gochain-minute-leap-v2 are not imported. This supplies
technical trading liquidity only; it does not create external demand or value.

## Build and run

From C:\Projects\nothingchain:

```powershell
go build -tags amm -o gochain-amm.exe .
# Create only once. Existing wallet files are never overwritten.
.\gochain-amm.exe wallet create --file amm-provider.wallet.json
.\gochain-amm.exe serve --port 8082 --wallet amm-provider.wallet.json
```

The test server uses gochain-amm-test.json and does NOT automatically mine.
Keep the wallet file private and back it up. It contains an unencrypted seed.
The ordinary build keeps the previous network ID and ledger unchanged.
Do not run both network builds against the same ledger file.

## Inspect and trade (another PowerShell window)

```powershell
.\gochain-amm.exe amm status
.\gochain-amm.exe amm quote --asset testUSD --a 100
.\gochain-amm.exe amm swap --wallet amm-provider.wallet.json --asset testUSD --a 100 --min-a 0.8
.\gochain-amm.exe mine --wallet amm-provider.wallet.json
.\gochain-amm.exe amm status
```

Quotes use confirmed reserves. Choose your own minimum output from the current
quote; the example minimum is not a recommendation. Swaps are queued until mined.
Amounts use eight decimal places. GO input uses --asset GO. All write commands
require the wallet and sign the full operation, including limits and expiry.
--ttl defaults to 20 blocks (range 1..1000), not wall-clock minutes.

## Deposit and withdraw

For a fresh pool, mine enough GO, claim testUSD, then seed both sides:

```powershell
.\gochain-amm.exe mine --wallet amm-provider.wallet.json
.\gochain-amm.exe mine --wallet amm-provider.wallet.json
.\gochain-amm.exe amm faucet --wallet amm-provider.wallet.json
.\gochain-amm.exe mine --wallet amm-provider.wallet.json
.\gochain-amm.exe amm add --wallet amm-provider.wallet.json --a 20 --b 2000 --min-a 199
.\gochain-amm.exe mine --wallet amm-provider.wallet.json
```

For add, --a/--b are maximum GO/testUSD deposits, and --min-a is minimum LP shares.
Later deposits use the pool ratio; unused amounts stay in the wallet. Shares are
not dollars. For remove, --a is LP shares to burn and --min-a/--min-b are minimum
GO/testUSD returned:

```powershell
.\gochain-amm.exe amm remove --wallet amm-provider.wallet.json --a 10 --min-a 0.8 --min-b 90
.\gochain-amm.exe mine --wallet amm-provider.wallet.json
```

The demonstration already seeded the local ledger; do not repeat initial setup
expecting a fresh pool. Inspect status first.

## Rules and boundaries

- 0.3% swap fee stays in reserves for liquidity providers. Output is rounded down.
- Output = floor(input * 997 * outputReserve / (inputReserve * 1000 + input * 997)).
- Initial shares = floor(sqrt(GO atoms * testUSD atoms)); 1,000 share atoms are
  permanently locked. Later share issuance rounds down and deposits round up.
- Withdrawals round down. Tiny swaps/deposits/withdrawals that produce zero fail.
- Products use big integers, stored amounts are bounded signed 64-bit integers.
- Signatures, sequential nonces, balances, slippage, expiry, and conservation are
  checked through ledger replay. Failed operations are not persisted.
- The faucet issues 10,000 testUSD once per address, up to 100 million total.
  Anyone can generate new addresses: this is deliberately test-only issuance.
- Protocol transactions use canonical JSON prefixed with amm: in a signed memo,
  wrapped in a zero-amount self-transfer. Ordinary GO transfers remain supported.
- GET /amm gives confirmed reserves and LP/testUSD balances. Wallet secrets are
  never served. The existing explorer shows operation details in transaction memos;
  a dedicated swap form is not implemented yet. The CLI is the signing interface.
- Existing fixed-difficulty consensus remains experimental. There is no external
  price oracle, dollar peg, lending, MEV protection, or production security claim.
  Pool price can be moved by trades. Providers bear price and liquidity risks.

## Verification

```powershell
go test ./...
go test -tags amm ./...
go vet -tags amm ./...
go test -race -tags amm ./...
```

Tests cover pool lifecycle, fee/product behavior, conservation, withdrawal
ownership, slippage, expiry, signatures, integer bounds, and restart replay.
The live demo deposited 20 GO / 2,000 testUSD, swapped 100 testUSD for
0.94965947 GO, and withdrew 10 LP shares. Remaining reserves were
18.09782351 GO / 1,995 testUSD immediately after that demo.

## Deposit new mining rewards into liquidity

Run `python scripts/reward_liquidity.py` from the repository directory while the
AMM miner is running. This optional wallet-side helper uses the existing signed
CLI and changes no consensus rules. Python's standard library is sufficient.

Defaults: allocate 20% of rewards mined AFTER the initial baseline, wait for two
confirmations, submit ONE deposit capped at 2 GO and 500 testUSD, and exit after
two confirmations of that deposit (or ten minutes). It uses existing matching
testUSD; it never claims faucet tokens or sells GO to fund the deposit. Shares
must be at least 99% of the calculated quote. The maximum inputs are authorized
limits; rounding can leave dust in the wallet. Two confirmations are not finality.

The helper skips when funds are insufficient or transactions are pending. Keep
other signing tools for this wallet idle during a run. A saved journal records
the baseline, limits, proposal, signature and confirmation. Reusing the default
journal after completion does not spend again. Inspect it before deliberately
starting another bounded run with a different --state path; budget caps apply
PER JOURNAL, not globally across different files. Custom journals and lock files
must remain private/local and must not be committed.

If interrupted during submission, the prepared journal blocks automatic retries:
check the ledger manually to avoid a duplicate deposit. An orphaned or expired
queued transaction also requires manual review; the helper never blindly
resubmits it. A baseline reorganization stops the run. A stale .lock file may be
removed only after verifying the old helper process has stopped. API failures
stop the helper. Do not treat this as unattended production treasury software.

Useful flags: --percent, --go-budget, --usd-budget, --confirmations, --duration,
--node, --wallet, --binary, --state. Change a saved journal's configuration only
by explicitly starting a new run with a new journal, not by editing its contents.

Tests: `python -m unittest discover -s scripts -p test_reward_liquidity.py -v`

## Repeatable six-wallet trading simulation

Build a separate binary and run offline against a NEW output directory:

```powershell
go build -tags amm -o gochain-sim.exe .
.\gochain-sim.exe simulate --seed 42 --attempts 100 --out simulation-local
```

No running server is required. The harness invokes the actual signed transaction,
AMM validation, proof-of-work, and persistence code on its own ledger. Six trading
wallets start with 50 GO / 10,000 testUSD each. Two buy, two sell, one mixes both,
and one attempts bounded trades only when profitable against a synthetic external
price. A separate LP seeds 100 GO / 10,000 testUSD; subsequent mining rewards go
to another account so they do not inflate reported trader or LP performance.

The reference price starts at 100 testUSD/GO and follows seeded integer steps of
up to 2% per attempt, bounded to 50..200. It is only used by the simulator and
never enters consensus. Every seventeenth attempt deliberately requests one atom
more than the quote to test rejection. Other orders use a 1% minimum-output limit.
Each successful order is mined before the next attempt; this does not model
concurrent order flow, MEV, network latency, gas or external exchange costs.

The output directory contains ledger.json and results.json, including every trade
attempt, quoted output, outcome, reference price, final wallet balances, pool
reserves, input-denominated fees, marked volume, and LP value versus holding.
LP-minus-hold includes fees and initial locked-share dilution and is NOT pure
impermanent loss or a risk-adjusted return. One scenario cannot establish an
investment result or justify adaptive fees. Adaptive fees are not implemented.

Simulation timestamps use a fixed historical clock, and keys are public seeded
fixtures. NEVER send real assets or publish this ledger to live peers. The
harness makes no HTTP requests and requires a fresh output directory. The default
simulation-local directory is ignored by Git; explicitly keep custom outputs
outside the repository or add them to local ignore rules.

The automated reproducibility test repeats six attempts and compares all economic
results. Expanded AMM tests cover pending and confirmed nonce replay, rejection
without state mutation, tiny and oversized inputs, and confirmed reserve recovery
after a competing fork, including requeued orphan transactions.
