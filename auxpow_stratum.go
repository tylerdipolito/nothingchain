package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// StratumRPCRequest represents an incoming JSON-RPC line from a Stratum miner (e.g. Bitaxe).
type StratumRPCRequest struct {
	ID     any             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// StratumRPCResponse represents a JSON-RPC response returned to the miner.
type StratumRPCResponse struct {
	ID     any    `json:"id"`
	Result any    `json:"result"`
	Error  any    `json:"error"`
}

// StratumRPCNotification represents a server-to-miner notification (id is null).
type StratumRPCNotification struct {
	ID     any    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

// StratumJob represents an active mining work template.
type StratumJob struct {
	JobID          string
	CandidateBlock *Block
	GoTargetBits   uint32
	BtcPrevHash    string
	Coinb1         []byte
	Coinb2         []byte
	Version        uint32
	Bits           uint32
	Ntime          uint32
	CleanJobs      bool
}

// StratumSession tracks an active TCP connection to a miner.
type StratumSession struct {
	ID             uint64
	Conn           net.Conn
	Enc            *json.Encoder
	Worker         string
	Extranonce1    string
	Extranonce2Len int
	Difficulty     float64
	Subscribed     bool
	Authorized     bool
}

// AuxPoWStratumServer bridges Bitcoin ASIC miners (via Stratum v1) directly to GoChain AuxPoW consensus.
type AuxPoWStratumServer struct {
	ListenAddr     string
	GoChainNodeURL string
	DefaultMiner   string
	ShareDiff      float64 // Difficulty for Bitaxe shares (e.g. 1.0, 16.0, 64.0)
	GoTargetBits   uint32  // Target bits for GoChain block adoption

	mu              sync.RWMutex
	currentJob      *StratumJob
	jobHistory      map[string]*StratumJob
	submittedShares map[string]bool
	jobCounter      uint64
	sessionCounter  uint64
	sessions        map[uint64]*StratumSession
	shareHistory    []shareRecord
	minedBlocks     []minedBlockSummary

	AcceptedShares uint64
	RejectedShares uint64
	MinedBlocks    uint64

	OnBlockMined func(auxBlock *AuxBlock)
}

func NewAuxPoWStratumServer(addr, nodeURL, defaultMiner string, shareDiff float64, goBits uint32) *AuxPoWStratumServer {
	if shareDiff <= 0 {
		shareDiff = 1.0
	}
	if goBits == 0 {
		goBits = DefaultGoAuxBits
	}
	return &AuxPoWStratumServer{
		ListenAddr:      addr,
		GoChainNodeURL:  nodeURL,
		DefaultMiner:    defaultMiner,
		ShareDiff:       shareDiff,
		GoTargetBits:    goBits,
		jobHistory:      make(map[string]*StratumJob),
		submittedShares: make(map[string]bool),
		sessions:        make(map[uint64]*StratumSession),
	}
}

// DiffToTarget converts standard Stratum float difficulty to a 256-bit BigInt target.
// Base difficulty 1.0 corresponds to Bitcoin 0x1d00ffff:
// 0x00000000ffff0000000000000000000000000000000000000000000000000000
func DiffToTarget(diff float64) *big.Int {
	if diff <= 0 {
		diff = 1.0
	}
	diff1 := BitsToTarget(0x1d00ffff)
	diff1Float := new(big.Float).SetInt(diff1)
	targetFloat := new(big.Float).Quo(diff1Float, big.NewFloat(diff))
	target, _ := targetFloat.Int(nil)
	return target
}

// SwapWords32 reverses each 4-byte chunk in a 32-byte slice (Stratum prev_hash encoding).
func SwapWords32(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	for i := 0; i < len(out); i += 4 {
		end := i + 4
		if end > len(out) {
			end = len(out)
		}
		for j, k := i, end-1; j < k; j, k = j+1, k-1 {
			out[j], out[k] = out[k], out[j]
		}
	}
	return out
}

// FormatStratumPrevHash formats a standard big-endian Bitcoin block hash for Stratum mining.notify.
func FormatStratumPrevHash(rpcHashHex string) string {
	raw, err := hex.DecodeString(rpcHashHex)
	if err != nil || len(raw) != 32 {
		raw = make([]byte, 32)
	}
	// In wire order (little endian)
	wire := reverseBytes(raw)
	// Swap 4-byte words as required by Stratum v1 protocol
	swapped := SwapWords32(wire)
	return hex.EncodeToString(swapped)
}

// CreateNewJob generates a new Stratum job embedding candidate GoChain block in the parent coinbase.
func (s *AuxPoWStratumServer) CreateNewJob(candidate *Block, goBits uint32, clean bool) (*StratumJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.jobCounter++
	jobID := fmt.Sprintf("%x", s.jobCounter)

	candidate.Hash = candidate.computeHash()
	candidateHashBytes, err := hex.DecodeString(candidate.Hash)
	if err != nil {
		return nil, fmt.Errorf("invalid candidate hash: %w", err)
	}

	// 1. Build Coinbase Part 1 (everything up to extranonce1)
	// Version(4B) + InCount(1B) + PrevTx(32B) + PrevOutIndex(4B) + ScriptLen(1B) + Height(3B) + Tag + AuxPoWMagic(4B) + CandidateHash(32B)
	tag := []byte("/GoChain-Bitaxe/")
	scriptSigLen := byte(3 + len(tag) + len(AuxPoWMagic) + 32 + 4 + 4) // 4B extranonce1 + 4B extranonce2

	coinb1 := new(bytes.Buffer)
	coinb1.Write([]byte{0x01, 0x00, 0x00, 0x00}) // Tx Version 1
	coinb1.WriteByte(0x01)                       // 1 input
	coinb1.Write(make([]byte, 32))               // Previous out hash: all zeroes (coinbase)
	coinb1.Write([]byte{0xff, 0xff, 0xff, 0xff}) // Previous out index: 0xffffffff
	coinb1.WriteByte(scriptSigLen)
	coinb1.Write([]byte{0x03, 0x48, 0x03, 0x00}) // BIP34 dummy height
	coinb1.Write(tag)
	coinb1.Write(AuxPoWMagic)
	coinb1.Write(candidateHashBytes)

	// 2. Build Coinbase Part 2 (after extranonce2)
	// Sequence(4B) + OutCount(1B) + Value(8B) + ScriptLen(1B) + ScriptPubKey + Locktime(4B)
	coinb2 := new(bytes.Buffer)
	coinb2.Write([]byte{0x00, 0x00, 0x00, 0x00}) // Sequence
	coinb2.WriteByte(0x01)                       // 1 output
	// 50 BTC dummy subsidy (5,000,000,000 satoshis = 0x000000012a05f200)
	binary.Write(coinb2, binary.LittleEndian, uint64(5000000000))
	// Standard P2PKH script (25 bytes)
	p2pkh := []byte("\x19\x76\xa9\x14\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x88\xac")
	coinb2.Write(p2pkh)
	coinb2.Write([]byte{0x00, 0x00, 0x00, 0x00}) // Locktime

	btcPrevHash := "00000000000000000001928374a5b6c7d8e9f0123456789abcdef0123456789a"
	job := &StratumJob{
		JobID:          jobID,
		CandidateBlock: candidate,
		GoTargetBits:   goBits,
		BtcPrevHash:    btcPrevHash,
		Coinb1:         coinb1.Bytes(),
		Coinb2:         coinb2.Bytes(),
		Version:        0x20000000,
		Bits:           0x1d00ffff,
		Ntime:          uint32(time.Now().Unix()),
		CleanJobs:      clean,
	}

	s.currentJob = job
	s.jobHistory[jobID] = job

	// Keep history bounded to last 64 jobs
	if len(s.jobHistory) > 64 {
		for k := range s.jobHistory {
			if k != jobID {
				delete(s.jobHistory, k)
				if len(s.jobHistory) <= 64 {
					break
				}
			}
		}
	}

	return job, nil
}

// BroadcastJob pushes the current mining.notify job to all connected miners.
func (s *AuxPoWStratumServer) BroadcastJob(job *StratumJob) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, session := range s.sessions {
		if session.Subscribed && session.Authorized {
			go s.sendJobToSession(session, job)
		}
	}
}

func (s *AuxPoWStratumServer) sendJobToSession(session *StratumSession, job *StratumJob) {
	prevHashFormatted := FormatStratumPrevHash(job.BtcPrevHash)
	params := []any{
		job.JobID,
		prevHashFormatted,
		hex.EncodeToString(job.Coinb1),
		hex.EncodeToString(job.Coinb2),
		[]string{}, // Empty merkle branch (coinbase is sole tx in candidate parent)
		fmt.Sprintf("%08x", job.Version),
		fmt.Sprintf("%08x", job.Bits),
		fmt.Sprintf("%08x", job.Ntime),
		job.CleanJobs,
	}

	notify := StratumRPCNotification{
		ID:     nil,
		Method: "mining.notify",
		Params: params,
	}

	_ = session.Enc.Encode(notify)
}

// ListenAndServe launches the Stratum server listener.
func (s *AuxPoWStratumServer) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind Stratum port %s: %w", s.ListenAddr, err)
	}
	defer listener.Close()

	log.Printf("[STRATUM] 🚀 GoChain AuxPoW Stratum Server listening on %s", s.ListenAddr)
	log.Printf("[STRATUM] ⛏️  Share Difficulty: %.2f | Target Bits: 0x%08x", s.ShareDiff, s.GoTargetBits)

	// Ensure we have an initial candidate job ready
	if s.currentJob == nil {
		candidate := NewBlock(1, GenesisBlock().Hash, []Transaction{}, s.DefaultMiner)
		candidate.Timestamp = time.Now().Unix()
		candidate.Reward = rewardAt(candidate.Timestamp)
		targetBits := s.GoTargetBits
		if s.GoChainNodeURL != "" {
			var blocks []*Block
			if err := request(s.GoChainNodeURL, "/chain", nil, &blocks); err == nil && len(blocks) > 0 {
				tip := blocks[len(blocks)-1]
				candidate = NewBlock(tip.Index+1, tip.Hash, []Transaction{}, s.DefaultMiner)
				candidate.Timestamp = time.Now().Unix()
				candidate.Reward = rewardAt(candidate.Timestamp)
				targetBits = ExpectedDifficulty(blocks, candidate.Index, DefaultGoAuxBits)
				candidate.Bits = targetBits
				s.GoTargetBits = targetBits
			}
		}
		if _, err := s.CreateNewJob(candidate, targetBits, true); err != nil {
			return err
		}
	}

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			log.Printf("[STRATUM] Accept error: %v", err)
			continue
		}

		sessionID := atomic.AddUint64(&s.sessionCounter, 1)
		extranonce1 := fmt.Sprintf("%08x", sessionID)
		session := &StratumSession{
			ID:             sessionID,
			Conn:           conn,
			Enc:            json.NewEncoder(conn),
			Extranonce1:    extranonce1,
			Extranonce2Len: 4,
			Difficulty:     s.ShareDiff,
		}

		s.mu.Lock()
		s.sessions[sessionID] = session
		s.mu.Unlock()

		go s.handleMiner(session)
	}
}

func (s *AuxPoWStratumServer) handleMiner(session *StratumSession) {
	defer func() {
		session.Conn.Close()
		s.mu.Lock()
		delete(s.sessions, session.ID)
		s.mu.Unlock()
		log.Printf("[STRATUM] Miner disconnected: %s (Worker: %s)", session.Conn.RemoteAddr(), session.Worker)
	}()

	log.Printf("[STRATUM] 🔌 New Miner Connected from %s", session.Conn.RemoteAddr())
	scanner := bufio.NewScanner(session.Conn)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var req StratumRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("[STRATUM] Invalid JSON from %s: %s", session.Conn.RemoteAddr(), string(line))
			continue
		}

		s.handleRPCRequest(session, &req)
	}
}

func (s *AuxPoWStratumServer) handleRPCRequest(session *StratumSession, req *StratumRPCRequest) {
	switch req.Method {
	case "mining.subscribe":
		session.Subscribed = true
		// Result: [ [ ["mining.set_difficulty", sub_id1], ["mining.notify", sub_id2] ], extranonce1, extranonce2_size ]
		result := []any{
			[][]string{
				{"mining.set_difficulty", fmt.Sprintf("%x", session.ID)},
				{"mining.notify", fmt.Sprintf("%x", session.ID)},
			},
			session.Extranonce1,
			session.Extranonce2Len,
		}
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: result, Error: nil})

	case "mining.authorize":
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		if len(params) > 0 {
			session.Worker = params[0]
		}
		session.Authorized = true
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: true, Error: nil})

		log.Printf("[STRATUM] 🔑 Worker Authorized: '%s' from %s", session.Worker, session.Conn.RemoteAddr())

		// Send initial set_difficulty and first mining.notify
		_ = session.Enc.Encode(StratumRPCNotification{
			ID:     nil,
			Method: "mining.set_difficulty",
			Params: []any{session.Difficulty},
		})

		s.mu.RLock()
		job := s.currentJob
		s.mu.RUnlock()
		if job != nil {
			s.sendJobToSession(session, job)
		}

	case "mining.extranonce.subscribe":
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: true, Error: nil})

	case "mining.suggest_difficulty", "mining.suggest_target":
		if req.ID != nil {
			_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: true, Error: nil})
		}

	case "mining.configure":
		// BIP-310 version-rolling negotiation
		res := map[string]any{
			"version-rolling":      true,
			"version-rolling.mask": "1fffe000",
		}
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: res, Error: nil})

	case "mining.submit":
		s.handleSubmit(session, req)

	default:
		if req.ID != nil {
			_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: true, Error: nil})
		}
	}
}

func (s *AuxPoWStratumServer) handleSubmit(session *StratumSession, req *StratumRPCRequest) {
	// params: [worker_name, job_id, extranonce2_hex, ntime_hex, nonce_hex]
	var params []string
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 5 {
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid params", nil}})
		return
	}

	jobID := params[1]
	extranonce2Hex := params[2]
	ntimeHex := params[3]
	nonceHex := params[4]
	var vBitsLog string
	if len(params) > 5 {
		vBitsLog = params[5]
	}
	log.Printf("[DEBUG SUBMIT] jobID=%s, extranonce2=%s, ntime=%s, nonce=%s, vBits=%s (total params: %d)",
		jobID, extranonce2Hex, ntimeHex, nonceHex, vBitsLog, len(params))

	s.mu.RLock()
	job, exists := s.jobHistory[jobID]
	s.mu.RUnlock()

	if !exists {
		atomic.AddUint64(&s.RejectedShares, 1)
		log.Printf("[STRATUM] ❌ Reject: Job '%s' not found (known jobs: %d)", jobID, len(s.jobHistory))
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{21, "Job not found or stale", nil}})
		return
	}

	// 1. Reconstruct full Coinbase Transaction
	extranonce1Bytes, _ := hex.DecodeString(session.Extranonce1)
	extranonce2Bytes, err := hex.DecodeString(extranonce2Hex)
	if err != nil || len(extranonce2Bytes) != session.Extranonce2Len {
		atomic.AddUint64(&s.RejectedShares, 1)
		log.Printf("[STRATUM] ❌ Reject: Invalid extranonce2 '%s' (len %d, expected %d)", extranonce2Hex, len(extranonce2Bytes), session.Extranonce2Len)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid extranonce2", nil}})
		return
	}

	// Reject duplicate share
	shareKey := fmt.Sprintf("%s:%s:%s:%s", jobID, session.Extranonce1, extranonce2Hex, nonceHex)
	s.mu.Lock()
	if s.submittedShares[shareKey] {
		s.mu.Unlock()
		atomic.AddUint64(&s.RejectedShares, 1)
		log.Printf("[STRATUM] ❌ Reject: Duplicate share %s", shareKey)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{22, "Duplicate share", nil}})
		return
	}
	s.submittedShares[shareKey] = true
	if len(s.submittedShares) > 10000 {
		for k := range s.submittedShares {
			delete(s.submittedShares, k)
			if len(s.submittedShares) <= 5000 {
				break
			}
		}
	}
	s.mu.Unlock()

	coinbase := append([]byte{}, job.Coinb1...)
	coinbase = append(coinbase, extranonce1Bytes...)
	coinbase = append(coinbase, extranonce2Bytes...)
	coinbase = append(coinbase, job.Coinb2...)

	cbHash := DoubleSHA256(coinbase)
	merkleRootHex := hex.EncodeToString(reverseBytes(cbHash[:]))

	// 2. Parse ntime and nonce
	ntimeBytes, err := hex.DecodeString(ntimeHex)
	if err != nil || len(ntimeBytes) != 4 {
		atomic.AddUint64(&s.RejectedShares, 1)
		log.Printf("[STRATUM] ❌ Reject: Invalid ntime '%s'", ntimeHex)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid ntime", nil}})
		return
	}
	ntimeVal := binary.BigEndian.Uint32(ntimeBytes)

	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil || len(nonceBytes) != 4 {
		atomic.AddUint64(&s.RejectedShares, 1)
		log.Printf("[STRATUM] ❌ Reject: Invalid nonce '%s'", nonceHex)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid nonce", nil}})
		return
	}
	nonceVal := binary.BigEndian.Uint32(nonceBytes)

	// 3. Assemble 80-byte Bitcoin Header with BIP-310 / AsicBoost resolution
	const bip320Mask uint32 = 0x1fffe000
	candidateVersions := []int32{int32(job.Version)}

	if len(params) > 5 && len(params[5]) > 0 {
		if vBits, err := hex.DecodeString(params[5]); err == nil && len(vBits) == 4 {
			beVal := binary.BigEndian.Uint32(vBits)
			leVal := binary.LittleEndian.Uint32(vBits)
			candidateVersions = append(candidateVersions,
				int32((job.Version & ^bip320Mask)|(beVal&bip320Mask)),
				int32(beVal),
				int32((job.Version & ^bip320Mask)|(leVal&bip320Mask)),
				int32(leVal),
			)
		}
	}

	// Fallback: check the 16 hardware AsicBoost version masks (bits 13..16)
	for i := uint32(0); i < 16; i++ {
		candidateVersions = append(candidateVersions, int32(job.Version|(i<<13)))
	}

	shareTarget := DiffToTarget(session.Difficulty)
	goTarget := BitsToTarget(job.GoTargetBits)

	var btcHeader BitcoinHeader
	var parentHashHex string
	var parentHashInt *big.Int
	foundValid := false

	for _, v := range candidateVersions {
		h := BitcoinHeader{
			Version:    v,
			PrevBlock:  job.BtcPrevHash,
			MerkleRoot: merkleRootHex,
			Timestamp:  ntimeVal,
			Bits:       job.Bits,
			Nonce:      nonceVal,
		}
		phHex, err := h.Hash()
		if err != nil {
			continue
		}
		phBytes, _ := hex.DecodeString(phHex)
		phInt := new(big.Int).SetBytes(phBytes)

		if phInt.Cmp(shareTarget) <= 0 || phInt.Cmp(goTarget) <= 0 {
			btcHeader = h
			parentHashHex = phHex
			parentHashInt = phInt
			foundValid = true
			break
		}
	}

	if !foundValid {
		btcHeader = BitcoinHeader{
			Version:    int32(job.Version),
			PrevBlock:  job.BtcPrevHash,
			MerkleRoot: merkleRootHex,
			Timestamp:  ntimeVal,
			Bits:       job.Bits,
			Nonce:      nonceVal,
		}
		parentHashHex, _ = btcHeader.Hash()
		atomic.AddUint64(&s.RejectedShares, 1)
		log.Printf("[STRATUM] ❌ Reject: Low diff from %s! Parent Hash: %s, ShareTarget: %064x", session.Worker, parentHashHex, shareTarget)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{23, "Low difficulty share", nil}})
		return
	}

	// Share Accepted!
	atomic.AddUint64(&s.AcceptedShares, 1)
	s.recordShare(session.Difficulty)
	_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: true, Error: nil})
	log.Printf("[STRATUM] ✅ Share Accepted from %s! Parent Hash: %s", session.Worker, parentHashHex)

	// 5. Check if Share Meets GoChain Difficulty Target!
	if parentHashInt.Cmp(goTarget) <= 0 {
		auxpow := &AuxPoW{
			ParentHeader:   btcHeader,
			CoinbaseTxHex:  hex.EncodeToString(coinbase),
			CoinbaseBranch: []string{}, // Coinbase is only tx
			CoinbaseIndex:  0,
		}

		// Assign miner address from worker if valid GoChain address, else default
		minerAddr := s.DefaultMiner
		parts := strings.Split(session.Worker, ".")
		if len(parts) > 0 && validAddress(parts[0]) {
			minerAddr = parts[0]
		}
		job.CandidateBlock.Miner = minerAddr
		job.CandidateBlock.Bits = job.GoTargetBits
		job.CandidateBlock.AuxPoW = auxpow

		auxBlock := &AuxBlock{
			Block:  job.CandidateBlock,
			Bits:   job.GoTargetBits,
			AuxPoW: auxpow,
		}

		// Cryptographic verification
		if err := ValidateAuxPoW(auxBlock.Block, auxpow, job.GoTargetBits); err != nil {
			log.Printf("[STRATUM] ❌ Candidate block validation failed: %v", err)
			return
		}

		log.Printf("\n" + strings.Repeat("=", 64))
		log.Printf("[AUXPOW] 🎉 BITAXE SOLUTION FOUND FOR GOCHAIN BLOCK #%d! 🎉", auxBlock.Index)
		log.Printf("[AUXPOW] Bitcoin Parent Nonce: 0x%08x | Hash: %s", btcHeader.Nonce, parentHashHex)
		log.Printf("[AUXPOW] GoChain Target Met! Compact Bits: 0x%08x", job.GoTargetBits)
		log.Printf("[AUXPOW] Reward: %d atoms (~10 GO) -> %s", auxBlock.Reward, auxBlock.Miner)
		log.Printf(strings.Repeat("=", 64) + "\n")

		if s.OnBlockMined != nil {
			s.OnBlockMined(auxBlock)
		}

		// Submit to GoChain node if configured
		if s.GoChainNodeURL != "" {
			go s.submitBlockToNode(auxBlock, btcHeader.Nonce, parentHashHex)
		} else {
			atomic.AddUint64(&s.MinedBlocks, 1)
			s.recordMinedBlock(auxBlock, btcHeader.Nonce, parentHashHex)
		}
	}
}

func (s *AuxPoWStratumServer) submitBlockToNode(auxBlock *AuxBlock, parentNonce uint32, parentHash string) {
	// Post block to GoChain RPC
	payload, err := json.Marshal(auxBlock.Block)
	if err != nil {
		return
	}
	resp, err := http.Post(strings.TrimRight(s.GoChainNodeURL, "/")+"/block", "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[STRATUM] Failed to broadcast block to GoChain node: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[STRATUM] Node response: %s (%s)", resp.Status, string(body))

	if resp.StatusCode == http.StatusOK {
		atomic.AddUint64(&s.MinedBlocks, 1)
		s.recordMinedBlock(auxBlock, parentNonce, parentHash)
		log.Printf("[AUXPOW] ✅ GoChain Node ADOPTED Block #%d! (Nonce: 0x%08x)", auxBlock.Index, parentNonce)

		// Immediately query updated chain and broadcast new job with consensus-expected bits
		var blocks []*Block
		if err := request(s.GoChainNodeURL, "/chain", nil, &blocks); err == nil && len(blocks) > 0 {
			tip := blocks[len(blocks)-1]
			candidate := NewBlock(tip.Index+1, tip.Hash, []Transaction{}, s.DefaultMiner)
			candidate.Timestamp = time.Now().Unix()
			candidate.Reward = rewardAt(candidate.Timestamp)
			expectedBits := ExpectedDifficulty(blocks, candidate.Index, DefaultGoAuxBits)
			candidate.Bits = expectedBits
			job, err := s.CreateNewJob(candidate, expectedBits, true)
			if err == nil {
				s.GoTargetBits = expectedBits
				s.BroadcastJob(job)
				log.Printf("[STRATUM] 🔄 New Work Template Broadcast: Block #%d (Target Bits: 0x%08x)",
					candidate.Index, expectedBits)
			}
		}
	}
}

func runStratum(args []string) error {
	fs := flags("stratum")
	port := fs.Int("port", 3333, "Stratum TCP port for ASIC miners")
	host := fs.String("host", "0.0.0.0", "listen interface (0.0.0.0 for LAN/Wi-Fi)")
	node := fs.String("node", DefaultNode, "GoChain node URL")
	miner := fs.String("miner", "", "default reward wallet address")
	wallet := fs.String("wallet", WalletFile, "wallet file")
	diff := fs.Float64("diff", 16.0, "Stratum share difficulty for Bitaxe")
	bits := fs.Uint("bits", uint(DefaultGoAuxBits), "GoChain block target bits")
	dash := fs.Int("dash", 3334, "Web dashboard HTTP port")
	if err := fs.Parse(args); err != nil {
		return err
	}

	defaultMiner := *miner
	if defaultMiner == "" {
		w, err := LoadWallet(*wallet)
		if err == nil {
			defaultMiner = w.Address
		} else {
			defaultMiner = "go105425de308a0119d46ed34a3ac43dcb3d3db334a7acdf2aeaf047a368a9e9e02"
		}
	}

	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := NewAuxPoWStratumServer(addr, *node, defaultMiner, *diff, uint32(*bits))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Launch real-time web dashboard
	dashAddr := fmt.Sprintf("%s:%d", *host, *dash)
	go func() {
		if err := srv.StartDashboard(dashAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[DASHBOARD] Dashboard server error: %v", err)
		}
	}()

	// Periodic poller for GoChain node block updates
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		var lastTipHash string
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var blocks []*Block
				if err := request(*node, "/chain", nil, &blocks); err == nil && len(blocks) > 0 {
					tip := blocks[len(blocks)-1]
					if tip.Hash != lastTipHash {
						lastTipHash = tip.Hash
						candidate := NewBlock(tip.Index+1, tip.Hash, []Transaction{}, defaultMiner)
						candidate.Timestamp = time.Now().Unix()
						candidate.Reward = rewardAt(candidate.Timestamp)
						expectedBits := ExpectedDifficulty(blocks, candidate.Index, DefaultGoAuxBits)
						candidate.Bits = expectedBits
						job, err := srv.CreateNewJob(candidate, expectedBits, true)
						if err == nil {
							srv.GoTargetBits = expectedBits
							srv.BroadcastJob(job)
							log.Printf("[STRATUM] 🔄 New Work Template: GoChain Block #%d (Parent Tip: %s..., Target Bits: 0x%08x)",
								candidate.Index, tip.Hash[:8], expectedBits)
						}
					}
				}
			}
		}
	}()

	return srv.ListenAndServe(ctx)
}
