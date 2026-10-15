#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
network="${1:-devnet}"
case "$network" in
 localnet) rpc="http://127.0.0.1:8897" ;;
 devnet) rpc="https://api.devnet.solana.com" ;;
 *) echo "Choose devnet or localnet; mainnet is not supported" >&2; exit 1 ;;
esac
cargo build-sbf --tools-version v1.58 --manifest-path programs/goamm/Cargo.toml
solana program deploy --url "$rpc" --keypair .local/deployer.json --program-id .local/goamm-program.json target/deploy/goamm.so
