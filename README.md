# GoChain — one-minute local network experiment

GoChain is a Go proof-of-work prototype with Ed25519 wallets, signed transfers,
and two-node synchronization. Builds use only the Go standard library.

## Current economics

- Initial reward: **10 GO per block**; no premine or transaction fees.
- Halvings occur at **00:00 UTC on February 29**, starting in **2028**:
  10 GO → 5 GO → 2.5 GO → 1.25 GO. Non-leap century years such as 2100 are skipped.
- A GO contains **100,000,000 atomic units**. All ledger arithmetic is integer.
  Each halving rounds down at the atomic-unit level, eventually reaching zero.
- There is **no separately imposed hard supply cap**. Supply is the sum of
  validated block rewards. The earlier 28.28-million projection was an estimate
  for different timing assumptions, not a consensus limit.
- At the intended one-minute cadence, the initial rate is approximately 14,400
  GO/day, matching 100 GO per ten minutes. Actual issuance depends on blocks.

**One minute is currently a local mining timer, not adaptive PoW difficulty.**
The puzzle still requires four leading hexadecimal zeroes. With two automatic
miners or manual mining, blocks can arrive faster. This experiment measures
propagation, transfers, forks, and reconnects before adding difficulty adjustment.

## Compatibility and your wallet

The network ID is `gochain-minute-leap-v2`; the default ledger is
`gochain-v2.json`. Both `nothingchain.json` and the previous `gochain.json` stay
intact. Old balances are not imported or reinterpreted under the new rules.
The existing `.gochain-wallet.json` works with the same address on the new network;
signatures are domain-separated by network ID to prevent old-transaction replay.

## Start two nodes on Windows

Build once, then use separate terminals in this project directory:

```powershell
go build -o gochain.exe .

# Node A: one automatic miner, wallet created only if absent.
.\gochain.exe serve --port 8080 --mine --interval 1m --peers http://127.0.0.1:8081
```

```powershell
# Node B: independent ledger and validation, no automatic mining.
.\gochain.exe serve --port 8081 --data gochain-peer-v2.json --peers http://127.0.0.1:8080
```

The miner produces one block on startup and then at its interval. Nodes poll
configured peers every two seconds. Stop each with **Ctrl+C**. PC sleep pauses
mining. Neither process is installed as a Windows service or startup task.

Inspect both nodes:

- [Node A stats](http://localhost:8080/stats)
- [Node B stats](http://localhost:8081/stats)
- [Node A peer status](http://localhost:8080/peers)
- [Node B peer status](http://localhost:8081/peers)

Matching `tip_hash` values mean both are on the same chain tip. `/peers` shows
last successful sync, errors, round-trip time and adopted-chain count. Round-trip
measurements are not block propagation benchmarks.

## Wallets and fractional transfers

```powershell
.\gochain.exe wallet address
.\gochain.exe wallet create --file recipient.wallet.json
.\gochain.exe send --node http://127.0.0.1:8081 --to <recipient-address> --amount 0.25 --memo "across nodes"
.\gochain.exe mine
.\gochain.exe balance <recipient-address>
.\gochain.exe validate
```

Use the actual address printed by wallet creation. The sender's default wallet
is `.gochain-wallet.json`; use `--wallet PATH` to select another. `send` signs
locally, and only the public signed transaction is transmitted. Up to eight
decimal places are accepted; floating-point/exponential amount inputs are rejected.

The wallet contains an **unencrypted private seed**. Keep a private backup and
restrict access. Seed data is never printed by the CLI. Default wallets, files
ending in `.wallet.json`, and the standard ledger files are ignored by Git.

## API and amount units

- `GET /health`: network ID, mining mode, interval, default reward address.
- `GET /stats`: supply, reward, next halving, validity, target cadence, tip hash.
- `GET /chain`: block history; `GET /sync`: complete bounded-sync snapshot.
- `GET /peers`: configured local peers and recent synchronization results.
- `GET /balance/{address}`: confirmed/projected balances and next account nonce.
- `POST /tx`: complete signed transaction; `POST /mine`: `{}` or
  `{"miner":"<wallet-address>"}`.

Human-readable amounts (`balance`, `total_supply`, `block_reward`, etc.) are
**decimal strings in GO**. Exact integer counterparts end in `_atoms`.
Transactions and block headers use `amount_atoms` and `reward_atoms` exclusively.
Do not send a GO decimal in an atomic-unit field. The CLI converts for you.

## Validation and fork handling

Peers independently verify the network ID, fixed genesis, hashes, required work,
indices, links, chronological timestamps, reward schedule, signatures, account
nonces, balances, and arithmetic bounds. A timestamp more than two minutes ahead
of the receiver's clock is rejected. Pending transfers are validated too.

All blocks have equal required work in this prototype: more valid blocks win.
Equal-length forks use the lexicographically smaller tip hash as a deterministic
tie-break. Reorganizations recalculate balances, remove confirmed transactions
from the mempool, and requeue valid orphaned transfers within mempool bounds.

The leap-year rule follows **block timestamps**. Monotonic timestamps and a future
bound do not prove the real-world creation time of historical blocks. A miner
controlling a competing history can backdate it. Trusted time/checkpoint policy
and more adversarial review are needed before treating calendar issuance as a
secure public-network policy. There is no claim of finality after one block.

## Limits and persistence

This is a local experiment, not a public or economically secured network. Peer
URLs are explicitly configured loopback HTTP origins; discovery and remote peers
are not implemented. Sync pulls complete snapshots (at most 8 MiB and 10,000
blocks); it is not a scalable synchronization protocol. Mempools and blocks each
allow up to 1,000 transfers. Disconnected peers retry automatically.

The default server binds to loopback. Mining is a local administrative endpoint
without authentication. Wallet signatures authorize spending. There is no market
price, listing, redemption promise, or affiliation with another token named GO.

Writes are serialized and saved through a temporary file before replacement;
failed saves do not publish changed state. Invalid files fail closed. One writer
process is allowed per ledger. A forced stop can leave `<ledger>.lock`; confirm
its recorded PID has stopped before removing it. Normal shutdown removes it.
Use `mine --offline` only when the default ledger's server is stopped.

## Local dashboard

Open http://localhost:8080/ after starting the server (or the port of your other
node). The embedded dashboard shows the public reward address and balance, actual
automatic-mining schedule, supply, halving date, searchable blocks, transaction
details, and peer tip agreement. It refreshes every three seconds and marks stale
data when disconnected. The explorer API formats GO amounts as exact decimal
strings to avoid browser integer rounding.

The dashboard is read-only. It does not load wallet seeds, sign transfers, or
control mining. HTML, CSS, and JavaScript are embedded in the Go binary; no npm,
external assets, or separate web server are required. Rebuild after editing web
assets. The existing JSON endpoints remain available.

## Tests

```powershell
go test ./...
go vet ./...
go test -race ./...
```

Coverage includes fractional arithmetic, leap-day boundaries (including 2100),
reward forgery, invalid timestamps, signatures, replay, overspending, self-transfers,
persistence rollback/restart, concurrency, peer propagation, mempool relay,
competing forks, orphan recovery, reconnect catchup, and invalid peer histories.
The Windows race detector requires a compatible C compiler; normal builds do not.

## Isolated AMM experiment

See [AMM.md](AMM.md) for the separate GO/testUSD testnet, pool rules, build command, and CLI deposit/swap/withdraw workflow. The ordinary build and existing ledger remain on the original development network.

An opt-in [trusted-checkpoint test network](CHECKPOINTS.md) preserves calendar
halvings with signed operator anchors and fails closed at stale checkpoints or
halving boundaries. It has a separate chain ID, ledger and default port; it does
not migrate existing balances. Its authority is trusted, and fixed-difficulty
public consensus remains experimental.

See [LOCAL_RELEASE.md](LOCAL_RELEASE.md) for Windows startup, wallet backup,
transaction recovery, executed verification and the remaining local/testnet limits.

## Independent Solana AMM lab

See [solana-devnet/README.md](solana-devnet/README.md) for the separate Rust/Anchor
AMM, wallet-connected swap and liquidity interface, local Solana verification,
and pending public devnet funding step. It uses our own program, with no Orca
engine or SDK dependency. These SPL test tokens are separate from mined GO.

## Local GO → Solana bridge laboratory

A separate trusted local bridge prototype now locks newly mined checkpoint-network
GO, issues matching eight-decimal Solana wGO, and burns wGO to release GO. Its UI
and separate wGO/testUSD pool run on port 5180. Existing Demo GO and old GoChain
balances are not migrated. See [BRIDGE.md](BRIDGE.md) for startup, trust assumptions,
recovery and verification. This is local test infrastructure, not a public bridge.
