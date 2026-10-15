//go:build !amm && !checkpoint

package main

const ChainID = "gochain-minute-leap-v2"
const ChainFile = "gochain-v2.json"
const DefaultNode = "http://127.0.0.1:8080"
const AMMEnabled = false
const CheckpointRequired = false
