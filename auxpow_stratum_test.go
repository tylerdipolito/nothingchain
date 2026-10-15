package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestAuxPoWStratumServerEndToEnd(t *testing.T) {
	// 1. Setup in-memory Stratum Server on an ephemeral local port
	addr := "127.0.0.1:0"
	minerAddr := "go1506ef1879d748ce0713b0dd01da32ad2a4b1ce97b5a852ff18800c1e52982376"

	// Set accessible difficulty for fast test execution
	shareDiff := 0.001
	goBits := DefaultGoAuxBits // 0x1f00ffff

	srv := NewAuxPoWStratumServer(addr, "", minerAddr, shareDiff, goBits)

	// Grab a real port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	srv.ListenAddr = l.Addr().String()
	l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var minedBlock *AuxBlock
	minedChan := make(chan *AuxBlock, 1)
	srv.OnBlockMined = func(ab *AuxBlock) {
		minedChan <- ab
	}

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond) // Let server bind

	// 2. Connect Simulated Bitaxe Client
	conn, err := net.Dial("tcp", srv.ListenAddr)
	if err != nil {
		t.Fatalf("Bitaxe client failed to connect to Stratum server: %v", err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := bufio.NewReader(conn)

	// Step A: mining.subscribe
	subReq := map[string]any{
		"id":     1,
		"method": "mining.subscribe",
		"params": []any{"BitaxeUltra/2.1", nil, "127.0.0.1", 3333},
	}
	if err := enc.Encode(subReq); err != nil {
		t.Fatalf("failed to send subscribe: %v", err)
	}

	line, err := dec.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read subscribe response: %v", err)
	}

	var subResp struct {
		ID     int            `json:"id"`
		Result []any          `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal(line, &subResp); err != nil || len(subResp.Result) < 3 {
		t.Fatalf("invalid subscribe response: %s", string(line))
	}

	extranonce1 := subResp.Result[1].(string)
	extranonce2Size := int(subResp.Result[2].(float64))
	t.Logf("Bitaxe connected! Assigned Extranonce1: %s, Extranonce2 Size: %d", extranonce1, extranonce2Size)

	// Step B: mining.authorize
	authReq := map[string]any{
		"id":     2,
		"method": "mining.authorize",
		"params": []any{minerAddr + ".bitaxe_worker1", "x"},
	}
	if err := enc.Encode(authReq); err != nil {
		t.Fatalf("failed to send authorize: %v", err)
	}

	line, err = dec.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read authorize response: %v", err)
	}
	t.Logf("Bitaxe Authorized Response: %s", string(line))

	// Step C: Receive set_difficulty and mining.notify
	var notifyParams []any
	for i := 0; i < 2; i++ {
		line, err = dec.ReadBytes('\n')
		if err != nil {
			t.Fatalf("failed to read server notification: %v", err)
		}
		var notif struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.Unmarshal(line, &notif)
		if notif.Method == "mining.notify" {
			notifyParams = notif.Params
		}
	}

	if len(notifyParams) < 9 {
		t.Fatalf("did not receive valid mining.notify from Stratum server")
	}

	jobID := notifyParams[0].(string)
	prevHashStr := notifyParams[1].(string)
	coinb1Hex := notifyParams[2].(string)
	coinb2Hex := notifyParams[3].(string)
	versionHex := notifyParams[5].(string)
	bitsHex := notifyParams[6].(string)
	ntimeHex := notifyParams[7].(string)

	t.Logf("Received Stratum Job #%s! Version: %s, Bits: %s, Ntime: %s", jobID, versionHex, bitsHex, ntimeHex)

	// Step D: Simulate Bitaxe BM1366 ASIC Hashing
	coinb1, _ := hex.DecodeString(coinb1Hex)
	coinb2, _ := hex.DecodeString(coinb2Hex)
	extranonce1Bytes, _ := hex.DecodeString(extranonce1)
	extranonce2Bytes := []byte{0x00, 0x00, 0x00, 0x01} // 4 bytes

	fullCoinbase := append([]byte{}, coinb1...)
	fullCoinbase = append(fullCoinbase, extranonce1Bytes...)
	fullCoinbase = append(fullCoinbase, extranonce2Bytes...)
	fullCoinbase = append(fullCoinbase, coinb2...)

	cbHash := DoubleSHA256(fullCoinbase)
	merkleRoot := cbHash // sole tx

	// Parse previous block hash as bitaxe does (swap words)
	prevHashWireBytes, _ := hex.DecodeString(prevHashStr)
	prevHashWire := SwapWords32(prevHashWireBytes)

	ntimeBytes, _ := hex.DecodeString(ntimeHex)
	ntimeVal := binary.BigEndian.Uint32(ntimeBytes)

	bitsBytes, _ := hex.DecodeString(bitsHex)
	bitsVal := binary.BigEndian.Uint32(bitsBytes)

	versionBytes, _ := hex.DecodeString(versionHex)
	versionVal := binary.BigEndian.Uint32(versionBytes)

	goTarget := BitsToTarget(goBits)

	// ASIC Nonce search loop
	var solvedNonce uint32
	var found bool
	for n := uint32(0); n < 2_000_000; n++ {
		btcHeader := BitcoinHeader{
			Version:    int32(versionVal),
			PrevBlock:  hex.EncodeToString(reverseBytes(prevHashWire)),
			MerkleRoot: hex.EncodeToString(reverseBytes(merkleRoot[:])),
			Timestamp:  ntimeVal,
			Bits:       bitsVal,
			Nonce:      n,
		}

		rawHdr, _ := btcHeader.Serialize80Bytes()
		hSum := DoubleSHA256(rawHdr)
		hInt := new(big.Int).SetBytes(reverseBytes(hSum[:]))

		if hInt.Cmp(goTarget) <= 0 {
			solvedNonce = n
			found = true
			t.Logf("Bitaxe ASIC hit GoChain target! Nonce: %d (0x%08x), Hash: %064x", n, n, hInt)
			break
		}
	}

	if !found {
		t.Fatalf("ASIC loop exhausted without finding share")
	}

	// Step E: mining.submit
	nonceHex := fmt.Sprintf("%08x", solvedNonce)
	extranonce2Hex := hex.EncodeToString(extranonce2Bytes)

	submitReq := map[string]any{
		"id":     4,
		"method": "mining.submit",
		"params": []any{
			minerAddr + ".bitaxe_worker1",
			jobID,
			extranonce2Hex,
			ntimeHex,
			nonceHex,
		},
	}

	if err := enc.Encode(submitReq); err != nil {
		t.Fatalf("failed to send submit: %v", err)
	}

	line, err = dec.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read submit response: %v", err)
	}

	var submitResp struct {
		ID     int  `json:"id"`
		Result bool `json:"result"`
		Error  any  `json:"error"`
	}
	if err := json.Unmarshal(line, &submitResp); err != nil || !submitResp.Result {
		t.Fatalf("mining.submit was rejected: %s", string(line))
	}

	t.Logf("✅ Stratum Server Accepted Share: %s", string(line))

	// Step F: Verify GoChain AuxPoW Block was created and verified
	select {
	case minedBlock = <-minedChan:
		t.Logf("🎉 GoChain AuxPoW Block Successfully Generated from Bitaxe Share! Height: %d, Miner: %s, Reward: %d atoms",
			minedBlock.Index, minedBlock.Miner, minedBlock.Reward)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for block mined event")
	}

	if minedBlock.Miner != minerAddr {
		t.Fatalf("expected miner %s, got %s", minerAddr, minedBlock.Miner)
	}

	// Step G: Check server stats
	if srv.AcceptedShares != 1 {
		t.Fatalf("expected 1 accepted share, got %d", srv.AcceptedShares)
	}
	if srv.MinedBlocks != 1 {
		t.Fatalf("expected 1 mined block, got %d", srv.MinedBlocks)
	}
}
