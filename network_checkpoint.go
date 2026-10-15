//go:build checkpoint

package main

// A separate signature/hash domain and ledger; never reuse a legacy ledger.
const ChainID = "gochain-checkpoint-amm-test-v1"
const ChainFile = "gochain-checkpoint-test.json"
const DefaultNode = "http://127.0.0.1:8084"
const AMMEnabled = true
const CheckpointRequired = true
