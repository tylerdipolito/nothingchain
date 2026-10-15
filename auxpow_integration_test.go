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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuxPoWIsolatedEndToEndLifecycle(t *testing.T) {
	// 1. Create completely isolated test ledger and environment
	tempDir := t.TempDir()
	ledgerPath := filepath.Join(tempDir, "isolated_auxpow_chain.json")

	bc, err := LoadOrCreate(ledgerPath)
	if err != nil {
		t.Fatalf("failed to create isolated blockchain: %v", err)
	}

	// Create fresh test miner wallet
	minerWallet, err := CreateWallet(filepath.Join(tempDir, "miner_wallet.json"))
	if err != nil {
		t.Fatalf("failed to create miner wallet: %v", err)
	}
	minerAddr := minerWallet.Address

	// Initial balance must be 0
	acct, err := bc.Account(minerAddr)
	if err != nil {
		t.Fatalf("failed to query initial account: %v", err)
	}
	if acct.Balance != 0 {
		t.Fatalf("initial balance must be 0, got %d", acct.Balance)
	}

	// 2. Launch Isolated GoChain Node HTTP Server on ephemeral port
	nodeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind node listener: %v", err)
	}
	nodeAddr := "http://" + nodeListener.Addr().String()

	nodeHandler := newHandler(bc, minerAddr, false, time.Minute)
	nodeServer := &http.Server{Handler: nodeHandler}
	go func() {
		_ = nodeServer.Serve(nodeListener)
	}()
	defer nodeServer.Close()

	t.Logf("Isolated GoChain Node running at %s (Ledger: %s)", nodeAddr, ledgerPath)

	// 3. Launch Isolated AuxPoW Stratum Server on ephemeral port
	stratumListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind stratum listener: %v", err)
	}
	stratumAddr := stratumListener.Addr().String()
	stratumListener.Close() // Stratum server will bind this address

	goBits := DefaultGoAuxBits // 0x1f00ffff
	shareDiff := 0.001

	stratumServer := NewAuxPoWStratumServer(stratumAddr, nodeAddr, minerAddr, shareDiff, goBits)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	blockMinedChan := make(chan *AuxBlock, 1)
	stratumServer.OnBlockMined = func(ab *AuxBlock) {
		blockMinedChan <- ab
	}

	go func() {
		_ = stratumServer.ListenAndServe(ctx)
	}()

	time.Sleep(50 * time.Millisecond) // Let servers bind

	// 4. Connect Simulated Bitaxe Client to Stratum Server
	conn, err := net.Dial("tcp", stratumAddr)
	if err != nil {
		t.Fatalf("Bitaxe failed to connect to Stratum server: %v", err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := bufio.NewReader(conn)

	// --- Step 4.1: Handshake (mining.subscribe) ---
	subReq := map[string]any{
		"id":     1,
		"method": "mining.subscribe",
		"params": []any{"BitaxeUltra/BM1366", nil, "127.0.0.1", 3333},
	}
	if err := enc.Encode(subReq); err != nil {
		t.Fatalf("failed to send subscribe: %v", err)
	}

	subLine, err := dec.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read subscribe response: %v", err)
	}

	var subResp struct {
		ID     int   `json:"id"`
		Result []any `json:"result"`
	}
	if err := json.Unmarshal(subLine, &subResp); err != nil || len(subResp.Result) < 3 {
		t.Fatalf("invalid subscribe response: %s", string(subLine))
	}
	extranonce1 := subResp.Result[1].(string)
	extranonce2Len := int(subResp.Result[2].(float64))
	t.Logf("[STRATUM HANDSHAKE] Verified: Extranonce1=%s, Extranonce2Len=%d", extranonce1, extranonce2Len)

	// --- Step 4.2: Authorization (mining.authorize) ---
	authReq := map[string]any{
		"id":     2,
		"method": "mining.authorize",
		"params": []any{minerAddr + ".bitaxe01", "x"},
	}
	if err := enc.Encode(authReq); err != nil {
		t.Fatalf("failed to send authorize: %v", err)
	}

	authLine, err := dec.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read authorize response: %v", err)
	}
	t.Logf("[STRATUM AUTH] Verified: %s", strings.TrimSpace(string(authLine)))

	// --- Step 4.3: Receive Work Template (mining.notify) ---
	var notifyParams []any
	for i := 0; i < 2; i++ {
		notifLine, err := dec.ReadBytes('\n')
		if err != nil {
			t.Fatalf("failed to read notification: %v", err)
		}
		var notif struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.Unmarshal(notifLine, &notif)
		if notif.Method == "mining.notify" {
			notifyParams = notif.Params
		}
	}
	if len(notifyParams) < 9 {
		t.Fatalf("failed to receive valid mining.notify job")
	}

	jobID := notifyParams[0].(string)
	prevHashStr := notifyParams[1].(string)
	coinb1Hex := notifyParams[2].(string)
	coinb2Hex := notifyParams[3].(string)
	versionHex := notifyParams[5].(string)
	bitsHex := notifyParams[6].(string)
	ntimeHex := notifyParams[7].(string)

	t.Logf("[STRATUM JOB RECEIVED] JobID=%s, Version=%s, NBits=%s, NTime=%s", jobID, versionHex, bitsHex, ntimeHex)

	// --- Step 5: Test Adversarial Rejections ---
	t.Run("Reject_Malformed_Extranonce2", func(t *testing.T) {
		req := map[string]any{
			"id":     101,
			"method": "mining.submit",
			"params": []any{minerAddr, jobID, "00", ntimeHex, "12345678"}, // Too short extranonce2
		}
		_ = enc.Encode(req)
		respLine, _ := dec.ReadBytes('\n')
		var resp struct {
			Result bool `json:"result"`
			Error  any  `json:"error"`
		}
		_ = json.Unmarshal(respLine, &resp)
		if resp.Result {
			t.Fatalf("malformed extranonce2 must be rejected, got success")
		}
		t.Logf("✅ Malformed Extranonce2 correctly rejected: %s", strings.TrimSpace(string(respLine)))
	})

	t.Run("Reject_Stale_Nonexistent_JobID", func(t *testing.T) {
		req := map[string]any{
			"id":     102,
			"method": "mining.submit",
			"params": []any{minerAddr, "ffffffff", "00000001", ntimeHex, "12345678"},
		}
		_ = enc.Encode(req)
		respLine, _ := dec.ReadBytes('\n')
		var resp struct {
			Result bool `json:"result"`
			Error  any  `json:"error"`
		}
		_ = json.Unmarshal(respLine, &resp)
		if resp.Result {
			t.Fatalf("stale job ID must be rejected, got success")
		}
		t.Logf("✅ Stale/Nonexistent JobID correctly rejected: %s", strings.TrimSpace(string(respLine)))
	})

	t.Run("Reject_Low_Difficulty_Share", func(t *testing.T) {
		// Nonce 0 almost certainly does not meet difficulty target
		req := map[string]any{
			"id":     103,
			"method": "mining.submit",
			"params": []any{minerAddr, jobID, "00000001", ntimeHex, "00000000"},
		}
		_ = enc.Encode(req)
		respLine, _ := dec.ReadBytes('\n')
		var resp struct {
			Result bool `json:"result"`
			Error  any  `json:"error"`
		}
		_ = json.Unmarshal(respLine, &resp)
		if resp.Result {
			t.Fatalf("low difficulty share must be rejected, got success")
		}
		t.Logf("✅ Low difficulty share correctly rejected: %s", strings.TrimSpace(string(respLine)))
	})

	// --- Step 6: Solve Valid ASIC Work and Submit ---
	coinb1, _ := hex.DecodeString(coinb1Hex)
	coinb2, _ := hex.DecodeString(coinb2Hex)
	extranonce1Bytes, _ := hex.DecodeString(extranonce1)
	extranonce2Bytes := []byte{0x00, 0x00, 0x00, 0x02} // 4 bytes

	fullCoinbase := append([]byte{}, coinb1...)
	fullCoinbase = append(fullCoinbase, extranonce1Bytes...)
	fullCoinbase = append(fullCoinbase, extranonce2Bytes...)
	fullCoinbase = append(fullCoinbase, coinb2...)

	cbHash := DoubleSHA256(fullCoinbase)
	merkleRoot := cbHash

	prevHashWireBytes, _ := hex.DecodeString(prevHashStr)
	prevHashWire := SwapWords32(prevHashWireBytes)

	ntimeBytes, _ := hex.DecodeString(ntimeHex)
	ntimeVal := binary.BigEndian.Uint32(ntimeBytes)

	bitsBytes, _ := hex.DecodeString(bitsHex)
	bitsVal := binary.BigEndian.Uint32(bitsBytes)

	versionBytes, _ := hex.DecodeString(versionHex)
	versionVal := binary.BigEndian.Uint32(versionBytes)

	goTarget := BitsToTarget(goBits)

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
			t.Logf("[ASIC SOLUTION FOUND] Nonce: 0x%08x (%d), Parent Header Hash: %064x", n, n, hInt)
			break
		}
	}
	if !found {
		t.Fatalf("ASIC loop exhausted without finding valid share")
	}

	validExtranonce2Hex := hex.EncodeToString(extranonce2Bytes)
	validNonceHex := fmt.Sprintf("%08x", solvedNonce)

	// Submit valid share
	submitReq := map[string]any{
		"id":     104,
		"method": "mining.submit",
		"params": []any{
			minerAddr + ".bitaxe01",
			jobID,
			validExtranonce2Hex,
			ntimeHex,
			validNonceHex,
		},
	}
	if err := enc.Encode(submitReq); err != nil {
		t.Fatalf("failed to send valid submit: %v", err)
	}

	validRespLine, err := dec.ReadBytes('\n')
	if err != nil {
		t.Fatalf("failed to read valid submit response: %v", err)
	}

	var validResp struct {
		ID     int  `json:"id"`
		Result bool `json:"result"`
		Error  any  `json:"error"`
	}
	if err := json.Unmarshal(validRespLine, &validResp); err != nil || !validResp.Result {
		t.Fatalf("valid share was rejected: %s", string(validRespLine))
	}
	t.Logf("✅ Valid ASIC Share Accepted by Stratum Server: %s", strings.TrimSpace(string(validRespLine)))

	// --- Step 7: Test Duplicate Submission Rejection ---
	t.Run("Reject_Duplicate_Submission", func(t *testing.T) {
		// Resubmit identical share
		dupReq := map[string]any{
			"id":     105,
			"method": "mining.submit",
			"params": []any{
				minerAddr + ".bitaxe01",
				jobID,
				validExtranonce2Hex,
				ntimeHex,
				validNonceHex,
			},
		}
		_ = enc.Encode(dupReq)
		dupRespLine, _ := dec.ReadBytes('\n')
		var dupResp struct {
			Result bool `json:"result"`
			Error  any  `json:"error"`
		}
		_ = json.Unmarshal(dupRespLine, &dupResp)
		if dupResp.Result {
			t.Fatalf("duplicate share submission must be rejected, got success")
		}
		t.Logf("✅ Duplicate Share correctly rejected: %s", strings.TrimSpace(string(dupRespLine)))
	})

	// --- Step 8: Verify GoChain Node Adopts Block & Credits Wallet ---
	var minedBlock *AuxBlock
	select {
	case minedBlock = <-blockMinedChan:
		t.Logf("🎉 Stratum Server triggered OnBlockMined for Block #%d!", minedBlock.Index)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for block mined notification")
	}

	// Give node 200ms to process HTTP POST /block
	time.Sleep(200 * time.Millisecond)

	// Verify Node Tip via HTTP GET /chain
	var blocks []*Block
	if err := request(nodeAddr, "/chain", nil, &blocks); err != nil {
		t.Fatalf("failed to fetch /chain from isolated node: %v", err)
	}

	if len(blocks) != 2 { // Genesis (0) + Mined Block (1)
		t.Fatalf("expected chain length 2, got %d", len(blocks))
	}

	adoptedBlock := blocks[1]
	if adoptedBlock.Index != 1 {
		t.Fatalf("expected block index 1, got %d", adoptedBlock.Index)
	}
	if adoptedBlock.Miner != minerAddr {
		t.Fatalf("expected miner address %s, got %s", minerAddr, adoptedBlock.Miner)
	}
	if adoptedBlock.AuxPoW == nil {
		t.Fatalf("adopted block #1 is missing AuxPoW proof!")
	}

	// Verify cryptographic AuxPoW integrity on adopted block
	if err := ValidateAuxPoW(adoptedBlock, adoptedBlock.AuxPoW, adoptedBlock.Bits); err != nil {
		t.Fatalf("adopted block #1 failed ValidateAuxPoW: %v", err)
	}
	t.Logf("✅ Node Independently Validated AuxPoW Block #1: Hash=%s, ParentNonce=%d",
		adoptedBlock.Hash, adoptedBlock.AuxPoW.ParentHeader.Nonce)

	// Verify Wallet Reward via HTTP GET /balance/<minerAddr>
	var account Account
	if err := request(nodeAddr, "/balance/"+minerAddr, nil, &account); err != nil {
		t.Fatalf("failed to query /balance from node: %v", err)
	}

	expectedRewardAtoms := int64(1_000_000_000) // 10.00000000 GO
	if account.Balance != expectedRewardAtoms {
		t.Fatalf("expected wallet balance %d atoms (10 GO), got %d atoms", expectedRewardAtoms, account.Balance)
	}

	t.Logf("\n" + strings.Repeat("=", 70))
	t.Logf("🏆 FULL ASIC MINING LIFECYCLE VERIFIED ON ISOLATED TEST NETWORK!")
	t.Logf("   - Wallet Address: %s", minerAddr)
	t.Logf("   - Confirmed Balance: %d atoms (%.8f GO)", account.Balance, float64(account.Balance)/1e8)
	t.Logf("   - Block Height: %d", adoptedBlock.Index)
	t.Logf("   - Block Hash: %s", adoptedBlock.Hash)
	t.Logf("   - Bitcoin Parent Header Nonce: 0x%08x", adoptedBlock.AuxPoW.ParentHeader.Nonce)
	t.Logf("   - Coinbase Commitment: Valid (\\xfabe6d6d + GoBlockHash)")
	t.Logf(strings.Repeat("=", 70) + "\n")

	_ = os.Remove(ledgerPath)
}
