# GoChain Bitcoin AuxPoW (Merged Mining) Technical Specification & Verification Report

## Executive Summary

We have designed, implemented, and verified an end-to-end **Bitcoin-Compatible Auxiliary Proof-of-Work (AuxPoW / Merged Mining)** engine for GoChain in `C:\Projects\nothingchain`.

This implementation allows GoChain to borrow security from the global Bitcoin mining network. A Bitcoin ASIC miner mining BTC can simultaneously secure GoChain and earn native GO block rewards without splitting hashrate, buying new hardware, or sacrificing BTC mining revenue.

All 8 verification tests run in **0.129 seconds**, and all existing NothingChain regression suites remain **100% green**.

---

## 1. Merged Mining Architecture & Wire Mechanics

```
               +------------------------------------------------+
               |             Bitcoin Candidate Block            |
               |                                                |
               |  +------------------------------------------+  |
               |  |           Coinbase Transaction           |  |
               |  |                                          |  |
               |  |  scriptSig contains AuxPoW Marker:       |  |
               |  |  [\xfa\xbe\x6d\x6d] + [32B Go BlockHash] |  |
               |  +------------------------------------------+  |
               |                        |                       |
               |            Coinbase Merkle Branch              |
               |                        v                       |
               |  +------------------------------------------+  |
               |  |      Bitcoin 80-Byte Block Header        |  |
               |  |  - PrevBlock, MerkleRoot, Nonce, Bits    |  |
               |  +------------------------------------------+  |
               +------------------------------------------------+
                                        |
                 SHA-256(SHA-256(Header)) <= GoTarget?
                                        |
                   +--------------------+--------------------+
                   |                                         |
            YES (Bitcoin Met)                         YES (GoChain Met)
                   |                                         |
         Submitted to Bitcoin                     Submitted to GoChain
         Earns BTC Block Reward                   Earns 10 GO Block Reward
```

### The 4 Core Verification Pillars

1. **80-Byte Bitcoin Header Parsing & Hashing**:
   - Compliant with Bitcoin wire serialization: little-endian byte orders, 4-byte version, 32-byte previous block hash, 32-byte transaction Merkle root, 4-byte timestamp, 4-byte compact bits, 4-byte nonce.
   - Verified against Bitcoin Genesis Block #0 hash:
     `000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f`.

2. **Coinbase Commitment (`0xfabe6d6d`)**:
   - The Bitcoin coinbase transaction scriptSig includes the magic delimiter `0xfabe6d6d` followed by the 32-byte GoChain candidate block hash.
   - For multi-chain mining (securing GoChain + sibling chains simultaneously), the 32 bytes contain the root of an auxiliary Merkle tree.

3. **Merkle Proof Path (`CoinbaseBranch`)**:
   - Proves cryptographically that the coinbase transaction committing to GoChain was genuinely included in the parent Bitcoin header's `merkle_root`.

4. **Dual Difficulty & Cumulative Work Fork Choice**:
   - GoChain has its own independent difficulty target (compact bits).
   - Bitcoin's difficulty is typically much higher than GoChain's. A parent header whose hash satisfies GoChain's target bits creates a valid GoChain block, even if it does not meet Bitcoin's full difficulty.
   - Fork choice evaluates $\sum \frac{2^{256}}{\text{Target}_i + 1}$, guaranteeing that a shorter chain with higher proof-of-work dominates longer low-difficulty chains.

---

## 2. Benchmark & Test Verification Matrix

All tests executed via `go test -v -run "TestAuxPoW|TestBitcoinHeader|TestBits|TestMultiChain|TestDynamicDifficulty|TestCumulativeWork" .`:

| Test Suite | Execution Time | Result | Verification Scope |
| :--- | :--- | :--- | :--- |
| `TestBitcoinHeaderSerializationAndHash` | 0.00s | **PASS** | Validates exact 80-byte binary serialization against Bitcoin Block 0 Genesis hash. |
| `TestBitsAndTargetConversion` | 0.00s | **PASS** | Bidirectional 256-bit BigInt math and compact bits (`0x1d00ffff`, `0x1e00ffff`, `0x1f00ffff`). |
| `TestAuxPoWValidProofVerification` | 0.07s | **PASS** | AntPool_BTC simulates ASIC worker, finds nonce satisfying Go target, verified by GoChain. |
| `TestAuxPoWAdversarialRejections` | 0.02s | **PASS** | Rejects: (1) Insufficient work, (2) Mismatched block hash, (3) Corrupted coinbase tx, (4) Forged parent Merkle root. |
| `TestMultiChainMergedMiningSimulation` | 0.00s | **PASS** | Single Bitcoin block simultaneously commits to GoChain and Sibling Chain via Merkle tree branch. |
| `TestDynamicDifficultyRetargeting` | 0.00s | **PASS** | Tested 4x hashrate surge (target shrinks), 4x drop (target expands), and 4x clamp protections. |
| `TestCumulativeWorkForkChoice` | 0.00s | **PASS** | 3 high-diff blocks ($\text{Work} = 12,885,098,499$) selected over 10 low-diff blocks ($\text{Work} = 655,370$). |
| `TestAuxPoWBlockRewardAndSupplyConservation` | 0.02s | **PASS** | Pool reward credited correctly (10 GO initial issuance) conforming to halving schedule. |

**Full Regression Status**: `go test -tags "amm checkpoint" ./...` passed in **27.657s** with zero regressions.

---

## 3. Comparison of Implementation Models

| Dimension | Model A: Sovereign Multi-Tenant Generator | Model B: Shared Multi-Currency Engine | Model C: Merkle Hub / AuxPoW Syndicate (Recommended) |
| :--- | :--- | :--- | :--- |
| **Concept** | Each user launches an entirely new independent GoChain daemon & network. | One GoChain base layer; users issue custom sub-tokens directly on GoChain. | A single Bitcoin mining commitment tree secures unlimited independent GoChain-family sovereign chains. |
| **Consensus Security** | Hashrate fragmented across every new chain launched. | Full GoChain hashrate protects all assets on GoChain. | **Unified hashrate**: 1 Bitcoin hash simultaneously secures 1, 10, or 10,000 sovereign chains. |
| **Miner Incentive** | Miners must configure separate daemons for each chain. | Single daemon; miners earn native GO only. | Miners run one pool proxy; earn rewards from **all** participating chains at once. |
| **Solana / Cross-Chain Bridge** | Requires deploying a separate custody vault for every single chain. | Single custody vault bridges GO and all sub-assets to GoAMM. | Standardized bridge interface; each chain can have its own custody vault or shared relayer. |

---

## 4. Key Strategic Decision: Bitcoin (SHA-256) vs Litecoin (Scrypt)

- **GoChain is natively SHA-256**: Because GoChain already uses double SHA-256, it is **100% wire-compatible with Bitcoin (BTC)** right now without changing its hashing algorithm.
- **Dogecoin chose Litecoin because Dogecoin was Scrypt**: Dogecoin merged mined with Litecoin because both shared Scrypt.
- **GoChain's Advantage**: By targeting Bitcoin (SHA-256), GoChain has access to over **600 Exahashes/second (EH/s)** of global ASIC hashing power, the largest computational barrier in human history.
