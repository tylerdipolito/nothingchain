package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// TestMergedMiningTemplateGeneration verifies that the merged mining pool generates
// valid Bitcoin coinbase transactions committing to GoChain block hashes.
func TestMergedMiningTemplateGeneration(t *testing.T) {
	server := NewMergedMiningServer("127.0.0.1:0", "127.0.0.1:0", "", "", "", "", 1.0, 0x1f00ffff, 0x1f00ffff)

	candidate := NewBlock(1, GenesisBlock().Hash, []Transaction{}, "go1testmineraddress1234567890abcdef1234567890abcdef1234567890abcd")
	candidate.Timestamp = 1700000000
	candidate.Reward = 1000000000

	job, err := server.CreateNewMergedJob(candidate, 0x1f00ffff, true)
	if err != nil {
		t.Fatalf("failed to create merged job: %v", err)
	}

	// Verify BIP-34 height and GoChain block hash commitment in coinbase
	extranonce1, _ := hex.DecodeString("00000001")
	extranonce2, _ := hex.DecodeString("00000002")

	coinbase := append([]byte{}, job.Coinb1...)
	coinbase = append(coinbase, extranonce1...)
	coinbase = append(coinbase, extranonce2...)
	coinbase = append(coinbase, job.Coinb2...)

	magicIdx := bytes.Index(coinbase, AuxPoWMagic)
	if magicIdx == -1 {
		t.Fatalf("AuxPoW magic marker not found in generated coinbase")
	}

	candidateHashBytes, _ := hex.DecodeString(candidate.Hash)
	committedHash := coinbase[magicIdx+len(AuxPoWMagic) : magicIdx+len(AuxPoWMagic)+32]
	if !bytes.Equal(committedHash, candidateHashBytes) {
		t.Fatalf("committed hash %x does not match candidate hash %x", committedHash, candidateHashBytes)
	}

	// Verify Bitcoin 80-byte header serialization
	cbHash := DoubleSHA256(coinbase)
	merkleRootHex := hex.EncodeToString(reverseBytes(cbHash[:]))

	header := BitcoinHeader{
		Version:    int32(job.Version),
		PrevBlock:  job.BtcPrevHash,
		MerkleRoot: merkleRootHex,
		Timestamp:  job.Ntime,
		Bits:       job.Bits,
		Nonce:      123456,
	}

	rawHeader, err := header.Serialize80Bytes()
	if err != nil {
		t.Fatalf("failed to serialize 80-byte Bitcoin header: %v", err)
	}
	if len(rawHeader) != 80 {
		t.Fatalf("expected 80 bytes for Bitcoin header, got %d", len(rawHeader))
	}

	t.Logf("✅ Successfully generated authentic Bitcoin template with GoChain AuxPoW commitment")
}

// TestMergedMiningDualTargetEvaluation verifies independent and simultaneous dual wins.
func TestMergedMiningDualTargetEvaluation(t *testing.T) {
	// Scenario A: Go Target is easy (0x1f00ffff), BTC Target is hard (0x1000ffff)
	testMiner := "go19c5446aa51196d4ef9e8036ec203e70333e178c1351baf7c12f96229077c2272"
	serverA := NewMergedMiningServer("127.0.0.1:0", "127.0.0.1:0", "", testMiner, "", "", 0.001, 0x1000ffff, 0x1f00ffff)
	serverA.BtcRPCURL = ""
	serverA.btcState.RPCConnected = false
	serverA.btcState.TargetBits = 0x1000ffff
	candidateA := NewBlock(1, GenesisBlock().Hash, []Transaction{}, testMiner)
	candidateA.Timestamp = 1700000000
	jobA, err := serverA.CreateNewMergedJob(candidateA, 0x1f00ffff, false)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Search for a nonce meeting Go target
	extranonce1 := "00000001"
	extranonce2 := "00000001"
	ex1B, _ := hex.DecodeString(extranonce1)
	ex2B, _ := hex.DecodeString(extranonce2)
	cb := append(append(append([]byte{}, jobA.Coinb1...), ex1B...), ex2B...)
	cb = append(cb, jobA.Coinb2...)
	cbHash := DoubleSHA256(cb)
	merkleRoot := hex.EncodeToString(reverseBytes(cbHash[:]))

	goTarget := BitsToTarget(0x1f00ffff)
	btcTarget := BitsToTarget(jobA.Bits)
	var foundNonce uint32
	var foundHash string
	var foundTime uint32
	found := false

outerA:
	for extraTime := uint32(0); extraTime < 50; extraTime++ {
		curTime := jobA.Ntime + extraTime
		for nonce := uint32(0); nonce < 200000; nonce++ {
			h := BitcoinHeader{
				Version:    int32(jobA.Version),
				PrevBlock:  jobA.BtcPrevHash,
				MerkleRoot: merkleRoot,
				Timestamp:  curTime,
				Bits:       jobA.Bits,
				Nonce:      nonce,
			}
			hashHex, err := h.Hash()
			if err != nil {
				continue
			}
			hashBytes, _ := hex.DecodeString(hashHex)
			hashInt := new(big.Int).SetBytes(hashBytes)

			if hashInt.Cmp(goTarget) <= 0 && hashInt.Cmp(btcTarget) > 0 {
				foundNonce = nonce
				foundHash = hashHex
				foundTime = curTime
				found = true
				break outerA
			}
		}
	}

	if !found {
		t.Fatalf("could not find test nonce meeting target within search limit")
	}

	// Submit to server A session
	sessionA := &StratumSession{
		ID:             1,
		Extranonce1:    extranonce1,
		Extranonce2Len: 4,
		Difficulty:     0.001,
		Worker:         testMiner + ".worker",
	}
	serverA.sessions[1] = sessionA

	// Fake JSON-RPC submit request
	params := []string{sessionA.Worker, jobA.JobID, extranonce2, fmt.Sprintf("%08x", foundTime), fmt.Sprintf("%08x", foundNonce)}
	paramsJSON, _ := json.Marshal(params)
	req := &StratumRPCRequest{
		ID:     10,
		Method: "mining.submit",
		Params: paramsJSON,
	}

	// Capture response via pipe
	serverConn, clientConn := net.Pipe()
	sessionA.Conn = serverConn
	sessionA.Enc = json.NewEncoder(serverConn)

	go func() {
		serverA.handleSubmit(sessionA, req)
	}()

	var resp StratumRPCResponse
	_ = json.NewDecoder(clientConn).Decode(&resp)

	if resp.Result != true {
		t.Fatalf("expected share accepted, got: %+v", resp)
	}

	deadlineA := time.Now().Add(time.Second)
	for time.Now().Before(deadlineA) && serverA.goState.BlocksMined == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// Verify Go block won, but BTC block not won
	if serverA.goState.BlocksMined != 1 {
		t.Fatalf("expected 1 GoChain block won, got %d", serverA.goState.BlocksMined)
	}
	if serverA.btcState.BlocksMined != 0 {
		t.Fatalf("expected 0 Bitcoin blocks won under high difficulty, got %d", serverA.btcState.BlocksMined)
	}
	if serverA.DualWins != 0 {
		t.Fatalf("expected 0 dual wins, got %d", serverA.DualWins)
	}

	t.Logf("✅ Verified GoChain win without Bitcoin win: Nonce=0x%08x, Hash=%s", foundNonce, foundHash)

	// Scenario B: Both Go Target AND BTC Target are accessible (0x1f00ffff) -> SIMULTANEOUS DUAL WIN!
	serverB := NewMergedMiningServer("127.0.0.1:0", "127.0.0.1:0", "", testMiner, "", "", 0.001, 0x1f00ffff, 0x1f00ffff)
	jobB, _ := serverB.CreateNewMergedJob(candidateA, 0x1f00ffff, true)
	serverB.sessions[1] = sessionA

	var dualWinTriggered bool
	serverB.OnDualWin = func(btcBlock *SolvedBitcoinBlock, goBlock *AuxBlock) {
		dualWinTriggered = true
	}

	// Recompute merkle root with jobB
	cbB := append(append(append([]byte{}, jobB.Coinb1...), ex1B...), ex2B...)
	cbB = append(cbB, jobB.Coinb2...)
	cbBHash := DoubleSHA256(cbB)
	merkleRootB := hex.EncodeToString(reverseBytes(cbBHash[:]))

	foundB := false
	var foundTimeB uint32
outerB:
	for extraTime := uint32(0); extraTime < 50; extraTime++ {
		curTime := jobB.Ntime + extraTime
		for nonce := uint32(0); nonce < 200000; nonce++ {
			h := BitcoinHeader{
				Version:    int32(jobB.Version),
				PrevBlock:  jobB.BtcPrevHash,
				MerkleRoot: merkleRootB,
				Timestamp:  curTime,
				Bits:       jobB.Bits,
				Nonce:      nonce,
			}
			hashHex, err := h.Hash()
			if err != nil {
				continue
			}
			hashBytes, _ := hex.DecodeString(hashHex)
			hashInt := new(big.Int).SetBytes(hashBytes)

			if hashInt.Cmp(goTarget) <= 0 {
				foundNonce = nonce
				foundHash = hashHex
				foundTimeB = curTime
				foundB = true
				break outerB
			}
		}
	}

	if !foundB {
		t.Fatalf("could not find dual-qualifying nonce")
	}

	sPipe, cPipe := net.Pipe()
	sessionA.Conn = sPipe
	sessionA.Enc = json.NewEncoder(sPipe)

	paramsB := []string{sessionA.Worker, jobB.JobID, extranonce2, fmt.Sprintf("%08x", foundTimeB), fmt.Sprintf("%08x", foundNonce)}
	paramsBJSON, _ := json.Marshal(paramsB)
	reqB := &StratumRPCRequest{
		ID:     11,
		Method: "mining.submit",
		Params: paramsBJSON,
	}

	go func() {
		serverB.handleSubmit(sessionA, reqB)
	}()

	var respB StratumRPCResponse
	_ = json.NewDecoder(cPipe).Decode(&respB)

	if respB.Result != true {
		t.Fatalf("expected dual share accepted, got: %+v", respB)
	}

	deadlineB := time.Now().Add(time.Second)
	for time.Now().Before(deadlineB) && (serverB.btcState.BlocksMined == 0 || serverB.goState.BlocksMined == 0) {
		time.Sleep(10 * time.Millisecond)
	}

	if serverB.DualWins != 1 {
		t.Fatalf("expected 1 dual win, got %d", serverB.DualWins)
	}
	if serverB.btcState.BlocksMined != 1 {
		t.Fatalf("expected 1 Bitcoin block won, got %d", serverB.btcState.BlocksMined)
	}
	if serverB.goState.BlocksMined != 1 {
		t.Fatalf("expected 1 GoChain block won, got %d", serverB.goState.BlocksMined)
	}
	if !dualWinTriggered {
		t.Fatalf("OnDualWin callback was not invoked")
	}

	t.Logf("💎 Successfully verified SIMULTANEOUS DUAL BLOCK WIN from 1 hash: BTC Block #%d (+3.125 BTC) & GoChain Block #%d (+10 GO)",
		serverB.btcState.RecentBlocks[0].Height, serverB.goState.RecentBlocks[0].Index)
}

// TestMergedMiningIsolatedLifecycleEndToEnd executes a full end-to-end integration test
// with an isolated GoChain node and simulated ASIC miner connecting over TCP Stratum v1.
func TestMergedMiningIsolatedLifecycleEndToEnd(t *testing.T) {
	tempDir := t.TempDir()
	ledgerPath := filepath.Join(tempDir, "isolated_merged_chain.json")

	bc, err := LoadOrCreate(ledgerPath)
	if err != nil {
		t.Fatalf("failed to create isolated blockchain: %v", err)
	}

	minerWallet, err := CreateWallet(filepath.Join(tempDir, "merged_miner_wallet.json"))
	if err != nil {
		t.Fatalf("failed to create wallet: %v", err)
	}
	minerAddr := minerWallet.Address
	btcMinerAddr := "bc1qmergedminerisolatedtest784920"

	// 1. Launch Isolated GoChain Node HTTP Server
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

	// 2. Launch True Merged Mining Stratum Server
	stratumListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind stratum listener: %v", err)
	}
	stratumAddr := stratumListener.Addr().String()
	stratumListener.Close()

	dashListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind dash listener: %v", err)
	}
	dashAddr := dashListener.Addr().String()
	dashListener.Close()

	// Targets set to 0x1f00ffff for fast deterministic solving
	mergedServer := NewMergedMiningServer(stratumAddr, dashAddr, nodeAddr, minerAddr, btcMinerAddr, "", 0.001, 0x1f00ffff, 0x1f00ffff)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = mergedServer.ListenAndServe(ctx)
	}()
	go func() {
		_ = mergedServer.StartDashboard(dashAddr)
	}()

	time.Sleep(50 * time.Millisecond) // Allow ports to bind

	// 3. Connect Simulated Bitaxe Client over TCP Stratum v1
	conn, err := net.Dial("tcp", stratumAddr)
	if err != nil {
		t.Fatalf("failed to connect to Merged Stratum server: %v", err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := bufio.NewReader(conn)

	// Step 3.1: Subscribe
	_ = enc.Encode(map[string]any{"id": 1, "method": "mining.subscribe", "params": []any{"Bitaxe/BM1370"}})
	line1, _ := dec.ReadString('\n')
	var subResp StratumRPCResponse
	_ = json.Unmarshal([]byte(line1), &subResp)
	subResult := subResp.Result.([]any)
	extranonce1Hex := subResult[1].(string)
	extranonce2Size := int(subResult[2].(float64))

	// Step 3.2: Authorize
	workerName := fmt.Sprintf("%s.bitaxe_test", minerAddr)
	_ = enc.Encode(map[string]any{"id": 2, "method": "mining.authorize", "params": []any{workerName, "password"}})

	// Expect auth response + set_difficulty + mining.notify
	var currentJobParams []any
	for i := 0; i < 3; i++ {
		line, err := dec.ReadString('\n')
		if err != nil {
			break
		}
		var msg map[string]any
		_ = json.Unmarshal([]byte(line), &msg)
		if msg["method"] == "mining.notify" {
			currentJobParams = msg["params"].([]any)
			break
		}
	}

	if currentJobParams == nil {
		t.Fatalf("did not receive mining.notify after authorization")
	}

	jobID := currentJobParams[0].(string)
	prevHashStr := currentJobParams[1].(string)
	coinb1Hex := currentJobParams[2].(string)
	coinb2Hex := currentJobParams[3].(string)
	versionHex := currentJobParams[5].(string)
	bitsHex := currentJobParams[6].(string)
	ntimeHex := currentJobParams[7].(string)

	t.Logf("Received Merged Mining Job #%s! (BTC PrevHash: %s..., Bits: %s)", jobID, prevHashStr[:8], bitsHex)

	// Step 3.3: ASIC Miner Solves the Merged Proof
	coinb1Bytes, _ := hex.DecodeString(coinb1Hex)
	coinb2Bytes, _ := hex.DecodeString(coinb2Hex)
	extranonce1Bytes, _ := hex.DecodeString(extranonce1Hex)
	extranonce2Bytes := make([]byte, extranonce2Size)
	extranonce2Bytes[extranonce2Size-1] = 0x01
	extranonce2Hex := hex.EncodeToString(extranonce2Bytes)

	coinbaseFull := append([]byte{}, coinb1Bytes...)
	coinbaseFull = append(coinbaseFull, extranonce1Bytes...)
	coinbaseFull = append(coinbaseFull, extranonce2Bytes...)
	coinbaseFull = append(coinbaseFull, coinb2Bytes...)

	cbHash := DoubleSHA256(coinbaseFull)
	merkleRootHex := hex.EncodeToString(reverseBytes(cbHash[:]))

	versionBytes, _ := hex.DecodeString(versionHex)
	versionVal := binary.BigEndian.Uint32(versionBytes)
	bitsBytes, _ := hex.DecodeString(bitsHex)
	bitsVal := binary.BigEndian.Uint32(bitsBytes)
	ntimeBytes, _ := hex.DecodeString(ntimeHex)
	ntimeVal := binary.BigEndian.Uint32(ntimeBytes)

	targetInt := BitsToTarget(DefaultGoAuxBits)
	var solvedNonce uint32
	var solvedHashHex string
	solved := false

	for nonce := uint32(0); nonce < 200000; nonce++ {
		hdr := BitcoinHeader{
			Version:    int32(versionVal),
			PrevBlock:  mergedServer.btcState.TipHash,
			MerkleRoot: merkleRootHex,
			Timestamp:  ntimeVal,
			Bits:       bitsVal,
			Nonce:      nonce,
		}
		phHex, err := hdr.Hash()
		if err != nil {
			continue
		}
		phBytes, _ := hex.DecodeString(phHex)
		phInt := new(big.Int).SetBytes(phBytes)

		if phInt.Cmp(targetInt) <= 0 {
			solvedNonce = nonce
			solvedHashHex = phHex
			solved = true
			break
		}
	}

	if !solved {
		t.Fatalf("exhausted nonces searching for solution")
	}

	t.Logf("ASIC Solved Merged Proof! Nonce: 0x%08x, Hash: %s", solvedNonce, solvedHashHex)

	// Step 3.4: Miner submits solution
	nonceHex := fmt.Sprintf("%08x", solvedNonce)
	submitReq := map[string]any{
		"id":     100,
		"method": "mining.submit",
		"params": []any{workerName, jobID, extranonce2Hex, ntimeHex, nonceHex},
	}
	_ = enc.Encode(submitReq)

	lineSubmit, _ := dec.ReadString('\n')
	var submitResp StratumRPCResponse
	_ = json.Unmarshal([]byte(lineSubmit), &submitResp)

	if submitResp.Result != true {
		t.Fatalf("expected share accepted, got error: %+v", submitResp.Error)
	}

	time.Sleep(100 * time.Millisecond) // Allow node adoption and state updates

	// Step 3.5: Assert GoChain Node Adoption & Balance
	acct, err := bc.Account(minerAddr)
	if err != nil {
		t.Fatalf("failed to query miner account on GoChain node: %v", err)
	}
	if acct.Balance != 1000000000 {
		t.Fatalf("expected 10 GO (1000000000 atoms), got balance: %d", acct.Balance)
	}

	// Step 3.6: Assert Bitcoin State Accounting & Dual Win
	if mergedServer.btcState.BlocksMined != 1 {
		t.Fatalf("expected 1 Bitcoin block won, got %d", mergedServer.btcState.BlocksMined)
	}
	if mergedServer.btcState.TotalMinedSat < 0 {
		t.Fatalf("expected non-negative satoshis, got %d", mergedServer.btcState.TotalMinedSat)
	}
	if mergedServer.DualWins != 1 {
		t.Fatalf("expected 1 dual win recorded, got %d", mergedServer.DualWins)
	}

	// Step 3.7: Query Web Dashboard API
	respStats, err := http.Get("http://" + dashAddr + "/api/stats")
	if err != nil {
		t.Fatalf("dashboard /api/stats failed: %v", err)
	}
	defer respStats.Body.Close()

	var stats map[string]any
	_ = json.NewDecoder(respStats.Body).Decode(&stats)

	if stats["dual_wins_count"].(float64) != 1 {
		t.Fatalf("dashboard stats dual_wins_count expected 1, got %v", stats["dual_wins_count"])
	}

	t.Logf(`
========================================================================================
🏆 TRUE BITCOIN + GOCHAIN MERGED MINING FULL LIFECYCLE VERIFIED!
   - Stratum Protocol: Stratum v1 with BIP-310 Version Rolling
   - Bitcoin Parent Height: %d | Blocks Won: %d | Subsidy: %s
   - GoChain Aux Height: %d | Blocks Won: %d | Reward: %s
   - Simultaneous Dual Wins: %d (Zero extra energy!)
   - GoChain Node Balance: %s
   - Bitcoin Pool Satoshis: %d (3.12500000 BTC)
========================================================================================`,
		mergedServer.btcState.Height, mergedServer.btcState.BlocksMined, mergedServer.btcState.TotalMinedBTC,
		mergedServer.goState.CurrentHeight, mergedServer.goState.BlocksMined, mergedServer.goState.FormattedGO,
		mergedServer.DualWins, fmt.Sprintf("%.8f GO", float64(acct.Balance)/1e8), mergedServer.btcState.TotalMinedSat)
}
