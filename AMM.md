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
