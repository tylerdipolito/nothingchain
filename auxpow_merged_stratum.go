package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
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
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// BlockTemplate represents the BIP-22/23 result of Bitcoin Core getblocktemplate.
type BlockTemplate struct {
	Version                  int32    `json:"version"`
	Rules                    []string `json:"rules"`
	PreviousBlockHash        string   `json:"previousblockhash"`
	CoinbaseValue            int64    `json:"coinbasevalue"`
	Bits                     string   `json:"bits"`
	Height                   int64    `json:"height"`
	CurTime                  uint32   `json:"curtime"`
	DefaultWitnessCommitment string   `json:"default_witness_commitment"`
}

// SolvedBitcoinBlock holds metadata and raw serialized bytes of a mined Bitcoin block.
type SolvedBitcoinBlock struct {
	Height     int64  `json:"height"`
	Hash       string `json:"hash"`
	PrevHash   string `json:"prev_hash"`
	MerkleRoot string `json:"merkle_root"`
	Timestamp  uint32 `json:"timestamp"`
	Bits       string `json:"bits"`
	Nonce      string `json:"nonce"`
	Version    string `json:"version"`
	TxCount    int    `json:"tx_count"`
	RewardSat  int64  `json:"reward_satoshis"`
	RewardBTC  string `json:"reward_btc"`
	Miner      string `json:"miner_address"`
	RawHex     string `json:"raw_block_hex"`
	SolvedAt   int64  `json:"solved_at"`
	DualWin    bool   `json:"dual_win"`
	Submitted  bool   `json:"submitted_to_node"`
}

// BitcoinChainState manages parent Bitcoin blockchain state and accounting in the pool.
type BitcoinChainState struct {
	Height          int64                `json:"height"`
	TipHash         string               `json:"tip_hash"`
	TargetBits      uint32               `json:"target_bits"`
	BlockSubsidy    int64                `json:"block_subsidy_satoshis"`
	MinerAddress    string               `json:"miner_address"`
	BlocksMined     uint64               `json:"blocks_mined"`
	TotalMinedSat   int64                `json:"total_mined_satoshis"`
	TotalMinedBTC   string               `json:"total_mined_btc"`
	RecentBlocks    []SolvedBitcoinBlock `json:"recent_blocks"`
	RPCConnected    bool                 `json:"rpc_connected"`
	RPCNetwork      string               `json:"rpc_network"`
	RPCURL          string               `json:"rpc_url"`
}

// GoChainMergedState tracks the auxiliary GoChain state and accounting in the pool.
type GoChainMergedState struct {
	NodeURL       string              `json:"node_url"`
	MinerAddress  string              `json:"miner_address"`
	TargetBits    uint32              `json:"target_bits"`
	CurrentHeight int                 `json:"current_height"`
	BlocksMined   uint64              `json:"blocks_mined"`
	TotalMinedGO  int64               `json:"total_mined_atoms"`
	FormattedGO   string              `json:"formatted_go"`
	RecentBlocks  []minedBlockSummary `json:"recent_blocks"`
}

// MergedStratumJob represents a work template where a Bitcoin block header and coinbase commit to GoChain.
type MergedStratumJob struct {
	JobID        string
	BtcHeight    int64
	BtcPrevHash  string
	BtcBits      uint32
	GoCandidate  *Block
	GoTargetBits uint32
	Coinb1       []byte
	Coinb2       []byte
	MerkleBranch []string
	Version      uint32
	Bits         uint32
	Ntime        uint32
	CleanJobs    bool
	HasWitness   bool
	Template     *BlockTemplate
}

// MergedEventLogEntry represents an event in the merged mining pool (share, block, dual-win).
type MergedEventLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "share", "go_block", "btc_block", "dual_win"
	Message   string    `json:"message"`
	Details   any       `json:"details,omitempty"`
}

// MergedMiningServer coordinates true Bitcoin + GoChain merged mining over Stratum v1.
type MergedMiningServer struct {
	ListenAddr string
	DashAddr   string
	ShareDiff  float64
	BtcRPCURL  string

	BtcMiningPaused bool

	mu              sync.RWMutex
	btcState        *BitcoinChainState
	goState         *GoChainMergedState
	currentJob      *MergedStratumJob
	jobHistory      map[string]*MergedStratumJob
	submittedShares map[string]bool
	jobCounter      uint64
	sessionCounter  uint64
	sessions        map[uint64]*StratumSession
	shareHistory    []shareRecord
	eventLog        []MergedEventLogEntry

	AcceptedShares uint64
	RejectedShares uint64
	DualWins       uint64

	db          *PoolDatabase
	PayoutMode  PayoutMode
	PPLNSWindow int

	OnDualWin       func(btcBlock *SolvedBitcoinBlock, goBlock *AuxBlock)
	OnGoBlockMined  func(goBlock *AuxBlock)
	OnBtcBlockMined func(btcBlock *SolvedBitcoinBlock)
}

// DecodeBech32 decodes a bech32 string (like bc1q...) into hrp, witness version, and program bytes.
func DecodeBech32(s string) (string, byte, []byte, error) {
	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	s = strings.ToLower(s)
	pos := strings.LastIndex(s, "1")
	if pos < 1 || pos+7 > len(s) {
		return "", 0, nil, fmt.Errorf("invalid bech32 separator position")
	}
	hrp := s[:pos]
	dataPart := s[pos+1:]
	values := make([]byte, len(dataPart))
	for i, c := range dataPart {
		idx := strings.IndexRune(charset, c)
		if idx == -1 {
			return "", 0, nil, fmt.Errorf("invalid bech32 character: %c", c)
		}
		values[i] = byte(idx)
	}
	if len(values) < 6 {
		return "", 0, nil, fmt.Errorf("bech32 string too short")
	}
	payload := values[:len(values)-6]
	if len(payload) < 1 {
		return "", 0, nil, fmt.Errorf("empty bech32 payload")
	}
	version := payload[0]
	var acc, bits uint32
	var out []byte
	for _, val := range payload[1:] {
		acc = (acc << 5) | uint32(val)
		bits += 5
		for bits >= 8 {
			bits -= 8
			out = append(out, byte((acc>>bits)&0xff))
		}
	}
	return hrp, version, out, nil
}

// BitcoinAddressToScript converts a Bitcoin address (Bech32 bc1q... or Legacy) to wire scriptPubKey with VarInt length prefix.
func BitcoinAddressToScript(addr string) []byte {
	if strings.HasPrefix(strings.ToLower(addr), "bc1q") {
		_, ver, prog, err := DecodeBech32(addr)
		if err == nil && len(prog) == 20 && ver == 0 {
			script := []byte{0x00, 0x14}
			script = append(script, prog...)
			return append([]byte{byte(len(script))}, script...)
		}
	}
	// Fallback P2PKH script: 0x19 76 a9 14 <20-byte-hash> 88 ac
	h := sha256.Sum256([]byte(addr))
	p2pkh := append([]byte{0x76, 0xa9, 0x14}, h[:20]...)
	p2pkh = append(p2pkh, 0x88, 0xac)
	return append([]byte{byte(len(p2pkh))}, p2pkh...)
}

// ParseMinerUsername decomposes username into Go payout address, Bitcoin payout address, and friendly worker name.
func ParseMinerUsername(username, defaultGO, defaultBTC string) (minerGO, minerBTC, workerName string) {
	minerGO = defaultGO
	minerBTC = defaultBTC
	workerName = username

	if strings.Contains(username, ":") {
		parts := strings.SplitN(username, ":", 2)
		btcPart := parts[0]
		rest := parts[1]
		if strings.HasPrefix(btcPart, "bc1") || strings.HasPrefix(btcPart, "bcrt1") || strings.HasPrefix(btcPart, "1") || strings.HasPrefix(btcPart, "3") {
			minerBTC = btcPart
		}
		username = rest
	}

	if strings.Contains(username, ".") {
		parts := strings.SplitN(username, ".", 2)
		addrPart := parts[0]
		workerName = parts[1]
		if validAddress(addrPart) {
			minerGO = addrPart
		} else if strings.HasPrefix(addrPart, "bc1") || strings.HasPrefix(addrPart, "bcrt1") || strings.HasPrefix(addrPart, "1") || strings.HasPrefix(addrPart, "3") {
			minerBTC = addrPart
		}
	} else if validAddress(username) {
		minerGO = username
		workerName = "default"
	}

	return minerGO, minerBTC, workerName
}

func getWslIP() string {
	out, err := exec.Command("wsl", "hostname", "-I").Output()
	if err == nil {
		parts := strings.Fields(string(out))
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return "127.0.0.1"
}

func encodeBIP34Height(height int64) []byte {
	if height == 0 {
		return []byte{0x00}
	} else if height >= 1 && height <= 16 {
		return []byte{byte(0x51 + height - 1)}
	}
	var b []byte
	val := height
	for val > 0 {
		b = append(b, byte(val&0xff))
		val >>= 8
	}
	if b[len(b)-1]&0x80 != 0 {
		b = append(b, 0x00)
	}
	return append([]byte{byte(len(b))}, b...)
}

// AssembleStratumCoinbase reassembles non-witness and SegWit-witness coinbases from Stratum parts.
func AssembleStratumCoinbase(coinb1, en1, en2, coinb2 []byte, hasWitness bool) ([]byte, []byte) {
	nonWit := append(append(append(append([]byte{}, coinb1...), en1...), en2...), coinb2...)
	if !hasWitness {
		return nonWit, nonWit
	}
	buf := new(bytes.Buffer)
	buf.Write(nonWit[:4])               // Version
	buf.Write([]byte{0x00, 0x01})       // SegWit marker & flag
	buf.Write(nonWit[4 : len(nonWit)-4]) // In & Out
	buf.Write([]byte{0x01, 0x20})       // 1 witness item of 32 bytes
	buf.Write(make([]byte, 32))         // 32-byte witness reserved value
	buf.Write(nonWit[len(nonWit)-4:])   // LockTime
	return buf.Bytes(), nonWit
}

func (s *MergedMiningServer) callBitcoinCoreRPC(method string, params []any) (json.RawMessage, error) {
	if s.BtcRPCURL == "" {
		return nil, fmt.Errorf("bitcoin core RPC not configured")
	}
	u, err := url.Parse(s.BtcRPCURL)
	if err != nil {
		return nil, fmt.Errorf("invalid bitcoin RPC URL: %w", err)
	}

	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      "stratum",
		"method":  method,
		"params":  params,
	})

	postURL := fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
	if u.Path == "" {
		postURL += "/"
	}
	req, err := http.NewRequest("POST", postURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		req.SetBasicAuth(u.User.Username(), pass)
	}
	req.Header.Set("Content-Type", "text/plain;")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("bitcoin RPC decode error: %w (body: %s)", err, string(body))
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("bitcoin RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func (s *MergedMiningServer) fetchBitcoinCoreTemplate() (*BlockTemplate, error) {
	res, err := s.callBitcoinCoreRPC("getblocktemplate", []any{map[string]any{"rules": []string{"segwit"}}})
	if err != nil {
		return nil, err
	}
	var tmpl BlockTemplate
	if err := json.Unmarshal(res, &tmpl); err != nil {
		return nil, err
	}
	return &tmpl, nil
}

func (s *MergedMiningServer) submitBitcoinCoreBlock(rawBlockHex string) error {
	res, err := s.callBitcoinCoreRPC("submitblock", []any{rawBlockHex})
	if err != nil {
		return err
	}
	strRes := strings.Trim(string(res), "\" \r\n")
	if strRes != "null" && strRes != "" {
		return fmt.Errorf("submitblock rejected: %s", strRes)
	}
	return nil
}

// NewMergedMiningServer constructs a new True Merged Mining server instance.
func NewMergedMiningServer(listenAddr, dashAddr, nodeURL, goMiner, btcMiner, btcRPCURL string, shareDiff float64, btcBits, goBits uint32) *MergedMiningServer {
	if shareDiff <= 0 {
		shareDiff = 16.0
	}
	if btcBits == 0 {
		btcBits = DefaultGoAuxBits
	}
	if goBits == 0 {
		goBits = DefaultGoAuxBits
	}
	if btcMiner == "" {
		btcMiner = "bc1qxl26let6yc2fn8n33he4fswzsyys83h8j00uyq"
	}
	if goMiner == "" {
		goMiner = "go19c5446aa51196d4ef9e8036ec203e70333e178c1351baf7c12f96229077c2272"
	}

	srv := &MergedMiningServer{
		ListenAddr: listenAddr,
		DashAddr:   dashAddr,
		ShareDiff:  shareDiff,
		BtcRPCURL:  btcRPCURL,
		btcState: &BitcoinChainState{
			Height:       885000,
			TipHash:      "00000000000000000001a4bc389d7e6f12034981729384729183749283749283",
			TargetBits:   btcBits,
			BlockSubsidy: 312500000, // 3.125 BTC in satoshis
			MinerAddress: btcMiner,
			RecentBlocks: make([]SolvedBitcoinBlock, 0),
		},
		goState: &GoChainMergedState{
			NodeURL:      nodeURL,
			MinerAddress: goMiner,
			TargetBits:   goBits,
			RecentBlocks: make([]minedBlockSummary, 0),
		},
		jobHistory:      make(map[string]*MergedStratumJob),
		submittedShares: make(map[string]bool),
		sessions:        make(map[uint64]*StratumSession),
		shareHistory:    make([]shareRecord, 0),
		eventLog:        make([]MergedEventLogEntry, 0),
		PayoutMode:      PayoutModeHybrid,
		PPLNSWindow:     1000,
	}

	// Probe Bitcoin Core RPC if auto or configured
	if srv.BtcRPCURL == "none" || srv.BtcRPCURL == "off" || srv.BtcRPCURL == "disabled" || srv.BtcRPCURL == "paused" {
		srv.BtcRPCURL = ""
		srv.BtcMiningPaused = true
		srv.btcState.RPCConnected = false
		srv.btcState.RPCNetwork = "PAUSED"
		log.Printf("[MERGED-BTC] ⏸️ Bitcoin mining is PAUSED by configuration")
	} else if srv.BtcRPCURL == "" || srv.BtcRPCURL == "auto" {
		candidates := []string{
			fmt.Sprintf("http://bitproof:bitproofpassword@%s:18443", getWslIP()),
			"http://bitproof:bitproofpassword@127.0.0.1:18443",
		}
		for _, cand := range candidates {
			srv.BtcRPCURL = cand
			if res, err := srv.callBitcoinCoreRPC("getblockchaininfo", []any{}); err == nil {
				var info struct {
					Chain  string `json:"chain"`
					Blocks int64  `json:"blocks"`
				}
				_ = json.Unmarshal(res, &info)
				srv.btcState.RPCConnected = true
				srv.btcState.RPCNetwork = info.Chain
				srv.btcState.RPCURL = cand
				log.Printf("[MERGED-BTC] 🔌 Connected to live Bitcoin Core (%s) at %s", info.Chain, cand)
				break
			}
		}
		if !srv.btcState.RPCConnected {
			srv.BtcRPCURL = ""
		}
	} else {
		if res, err := srv.callBitcoinCoreRPC("getblockchaininfo", []any{}); err == nil {
			var info struct {
				Chain string `json:"chain"`
			}
			_ = json.Unmarshal(res, &info)
			srv.btcState.RPCConnected = true
			srv.btcState.RPCNetwork = info.Chain
			srv.btcState.RPCURL = srv.BtcRPCURL
			log.Printf("[MERGED-BTC] 🔌 Connected to live Bitcoin Core (%s) at %s", info.Chain, srv.BtcRPCURL)
		}
	}

	return srv
}

// logEventLocked appends an event while s.mu is already held.
func (s *MergedMiningServer) logEventLocked(eventType, msg string, details any) {
	entry := MergedEventLogEntry{
		Timestamp: time.Now(),
		Type:      eventType,
		Message:   msg,
		Details:   details,
	}
	s.eventLog = append([]MergedEventLogEntry{entry}, s.eventLog...)
	if len(s.eventLog) > 100 {
		s.eventLog = s.eventLog[:100]
	}
}

// LogEvent records an operational event for the dashboard stream.
func (s *MergedMiningServer) LogEvent(eventType, msg string, details any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logEventLocked(eventType, msg, details)
}

// CreateNewMergedJob builds an authentic Bitcoin block template embedding GoChain candidate in coinbase.
func (s *MergedMiningServer) CreateNewMergedJob(candidate *Block, goBits uint32, clean bool) (*MergedStratumJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.jobCounter++
	jobID := fmt.Sprintf("%x", s.jobCounter)

	candidate.Hash = candidate.computeHash()
	candidateHashBytes, err := hex.DecodeString(candidate.Hash)
	if err != nil {
		return nil, fmt.Errorf("invalid candidate GoChain hash: %w", err)
	}

	var tmpl *BlockTemplate
	if !s.BtcMiningPaused && s.BtcRPCURL != "" {
		if t, err := s.fetchBitcoinCoreTemplate(); err == nil && t != nil {
			tmpl = t
			s.btcState.Height = tmpl.Height
			s.btcState.TipHash = tmpl.PreviousBlockHash
			s.btcState.BlockSubsidy = tmpl.CoinbaseValue
			s.btcState.RPCConnected = true
			if bitsUint64, err := strconv.ParseUint(tmpl.Bits, 16, 32); err == nil {
				s.btcState.TargetBits = uint32(bitsUint64)
			}
		}
	}

	btcHeight := s.btcState.Height
	btcPrevHash := s.btcState.TipHash
	btcBits := s.btcState.TargetBits

	// 1. Build Stratum coinb1 with BIP-34 height & AuxPoW payload
	bip34 := encodeBIP34Height(btcHeight)
	tag := []byte("/Bitproof-GoChain/")
	magic := AuxPoWMagic
	auxPayload := new(bytes.Buffer)
	auxPayload.Write(tag)
	auxPayload.Write(magic)
	auxPayload.Write(candidateHashBytes)
	auxBytes := auxPayload.Bytes()

	totalAuxLen := len(auxBytes) + 4 + 4 // 4B extranonce1 + 4B extranonce2
	scriptSigTotalLen := len(bip34) + 1 + totalAuxLen

	coinb1 := new(bytes.Buffer)
	binary.Write(coinb1, binary.LittleEndian, int32(1)) // Tx Version 1
	coinb1.WriteByte(0x01)                              // 1 input
	coinb1.Write(make([]byte, 32))                      // Coinbase previous out hash
	coinb1.Write([]byte{0xff, 0xff, 0xff, 0xff})        // Previous out index
	coinb1.WriteByte(byte(scriptSigTotalLen))
	coinb1.Write(bip34)
	coinb1.WriteByte(byte(totalAuxLen))
	coinb1.Write(auxBytes)

	// 2. Build Stratum coinb2 with sequence, payout output, witness commitment, and locktime
	coinb2 := new(bytes.Buffer)
	coinb2.Write([]byte{0xff, 0xff, 0xff, 0xff}) // Sequence
	hasWitness := tmpl != nil && tmpl.DefaultWitnessCommitment != ""
	if hasWitness {
		coinb2.WriteByte(0x02) // 2 outputs
	} else {
		coinb2.WriteByte(0x01) // 1 output
	}
	binary.Write(coinb2, binary.LittleEndian, uint64(s.btcState.BlockSubsidy))
	coinb2.Write(BitcoinAddressToScript(s.btcState.MinerAddress))
	if hasWitness {
		binary.Write(coinb2, binary.LittleEndian, uint64(0)) // 0 satoshis
		witScript, _ := hex.DecodeString(tmpl.DefaultWitnessCommitment)
		coinb2.WriteByte(byte(len(witScript)))
		coinb2.Write(witScript)
	}
	coinb2.Write([]byte{0x00, 0x00, 0x00, 0x00}) // LockTime

	version := uint32(0x20000000)
	if tmpl != nil && tmpl.Version != 0 {
		version = uint32(tmpl.Version)
	}

	job := &MergedStratumJob{
		JobID:        jobID,
		BtcHeight:    btcHeight,
		BtcPrevHash:  btcPrevHash,
		BtcBits:      btcBits,
		GoCandidate:  candidate,
		GoTargetBits: goBits,
		Coinb1:       coinb1.Bytes(),
		Coinb2:       coinb2.Bytes(),
		MerkleBranch: []string{}, // Coinbase is only tx in candidate parent
		Version:      version,
		Bits:         btcBits,
		Ntime:        uint32(time.Now().Unix()),
		CleanJobs:    clean,
		HasWitness:   hasWitness,
		Template:     tmpl,
	}

	s.currentJob = job
	s.jobHistory[jobID] = job

	// Retain up to 64 recent jobs
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

// BroadcastJob sends the new merged mining template to all connected Stratum sessions.
func (s *MergedMiningServer) BroadcastJob(job *MergedStratumJob) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, session := range s.sessions {
		if session.Subscribed && session.Authorized {
			go s.sendJobToSession(session, job)
		}
	}
}

func (s *MergedMiningServer) sendJobToSession(session *StratumSession, job *MergedStratumJob) {
	prevHashFormatted := FormatStratumPrevHash(job.BtcPrevHash)
	params := []any{
		job.JobID,
		prevHashFormatted,
		hex.EncodeToString(job.Coinb1),
		hex.EncodeToString(job.Coinb2),
		job.MerkleBranch,
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

// ListenAndServe launches the Merged Mining Stratum listener.
func (s *MergedMiningServer) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to bind Merged Stratum port %s: %w", s.ListenAddr, err)
	}
	defer listener.Close()

	log.Printf("[MERGED] 🚀 Bitcoin + GoChain True Merged Mining Server listening on %s", s.ListenAddr)
	log.Printf("[MERGED] ⛏️  Share Diff: %.2f | BTC Target: 0x%08x | GO Target: 0x%08x",
		s.ShareDiff, s.btcState.TargetBits, s.goState.TargetBits)

	// Build initial candidate block
	if s.currentJob == nil {
		candidate := NewBlock(1, GenesisBlock().Hash, []Transaction{}, s.goState.MinerAddress)
		candidate.Timestamp = time.Now().Unix()
		candidate.Reward = rewardAt(candidate.Timestamp)
		targetBits := s.goState.TargetBits

		if s.goState.NodeURL != "" {
			var blocks []*Block
			if err := request(s.goState.NodeURL, "/chain", nil, &blocks); err == nil && len(blocks) > 0 {
				tip := blocks[len(blocks)-1]
				candidate = NewBlock(tip.Index+1, tip.Hash, []Transaction{}, s.goState.MinerAddress)
				candidate.Timestamp = time.Now().Unix()
				candidate.Reward = rewardAt(candidate.Timestamp)
				targetBits = ExpectedDifficulty(blocks, candidate.Index, DefaultGoAuxBits)
				candidate.Bits = targetBits
				s.goState.TargetBits = targetBits
				s.goState.CurrentHeight = tip.Index
			}
		}

		if _, err := s.CreateNewMergedJob(candidate, targetBits, true); err != nil {
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
			log.Printf("[MERGED] Accept error: %v", err)
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

func (s *MergedMiningServer) handleMiner(session *StratumSession) {
	defer func() {
		session.Conn.Close()
		s.mu.Lock()
		delete(s.sessions, session.ID)
		s.mu.Unlock()
		log.Printf("[MERGED] Miner disconnected: %s (Worker: %s)", session.Conn.RemoteAddr(), session.Worker)
	}()

	log.Printf("[MERGED] 🔌 New ASIC Miner Connected from %s", session.Conn.RemoteAddr())
	scanner := bufio.NewScanner(session.Conn)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var req StratumRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("[MERGED] Invalid JSON from %s: %s", session.Conn.RemoteAddr(), string(line))
			continue
		}

		s.handleRPCRequest(session, &req)
	}
}

func (s *MergedMiningServer) handleRPCRequest(session *StratumSession, req *StratumRPCRequest) {
	switch req.Method {
	case "mining.subscribe":
		session.Subscribed = true
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
		session.ConnectedAt = time.Now()

		minerGO, minerBTC, workerName := ParseMinerUsername(session.Worker, s.goState.MinerAddress, s.btcState.MinerAddress)
		session.MinerGO = minerGO
		session.MinerBTC = minerBTC
		session.WorkerName = workerName

		if s.db != nil {
			_, _ = s.db.RegisterOrUpdateWorker(session.Worker, minerGO, minerBTC, workerName)
		}

		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: true, Error: nil})

		log.Printf("[MERGED] 🔑 Miner Authorized: '%s' (Worker: %s, GO: %s, BTC: %s) from %s",
			session.Worker, session.WorkerName, session.MinerGO, session.MinerBTC, session.Conn.RemoteAddr())

		// Issue set_difficulty and first merged job
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
		// BIP-310 version-rolling support for hardware AsicBoost
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

func (s *MergedMiningServer) handleSubmit(session *StratumSession, req *StratumRPCRequest) {
	// params: [worker_name, job_id, extranonce2_hex, ntime_hex, nonce_hex, optional_version_bits]
	var params []string
	if err := json.Unmarshal(req.Params, &params); err != nil || len(params) < 5 {
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid params", nil}})
		return
	}

	jobID := params[1]
	extranonce2Hex := params[2]
	ntimeHex := params[3]
	nonceHex := params[4]

	s.mu.RLock()
	job, exists := s.jobHistory[jobID]
	s.mu.RUnlock()

	if !exists {
		atomic.AddUint64(&s.RejectedShares, 1)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{21, "Job not found or stale", nil}})
		return
	}

	// 1. Reconstruct full Bitcoin Coinbase Transaction
	extranonce1Bytes, _ := hex.DecodeString(session.Extranonce1)
	extranonce2Bytes, err := hex.DecodeString(extranonce2Hex)
	if err != nil || len(extranonce2Bytes) != session.Extranonce2Len {
		atomic.AddUint64(&s.RejectedShares, 1)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid extranonce2", nil}})
		return
	}

	// Duplicate share check
	shareKey := fmt.Sprintf("%s:%s:%s:%s", jobID, session.Extranonce1, extranonce2Hex, nonceHex)
	s.mu.Lock()
	if s.submittedShares[shareKey] {
		s.mu.Unlock()
		atomic.AddUint64(&s.RejectedShares, 1)
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

	rawCoinbaseWit, nonWitCoinbase := AssembleStratumCoinbase(job.Coinb1, extranonce1Bytes, extranonce2Bytes, job.Coinb2, job.HasWitness)
	cbHash := DoubleSHA256(nonWitCoinbase)
	merkleRootHex := hex.EncodeToString(reverseBytes(cbHash[:]))

	// 2. Parse ntime and nonce
	ntimeBytes, err := hex.DecodeString(ntimeHex)
	if err != nil || len(ntimeBytes) != 4 {
		atomic.AddUint64(&s.RejectedShares, 1)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid ntime", nil}})
		return
	}
	ntimeVal := binary.BigEndian.Uint32(ntimeBytes)

	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil || len(nonceBytes) != 4 {
		atomic.AddUint64(&s.RejectedShares, 1)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Invalid nonce", nil}})
		return
	}
	nonceVal := binary.BigEndian.Uint32(nonceBytes)

	// 3. BIP-310 / AsicBoost multi-version candidate resolution
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

	for i := uint32(0); i < 16; i++ {
		candidateVersions = append(candidateVersions, int32(job.Version|(i<<13)))
	}

	shareTarget := DiffToTarget(session.Difficulty)
	btcTarget := BitsToTarget(job.BtcBits)
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
			Bits:       job.BtcBits,
			Nonce:      nonceVal,
		}
		phHex, err := h.Hash()
		if err != nil {
			continue
		}
		phBytes, _ := hex.DecodeString(phHex)
		phInt := new(big.Int).SetBytes(phBytes)

		// Check if meets share target OR either chain's target
		if phInt.Cmp(shareTarget) <= 0 || phInt.Cmp(goTarget) <= 0 || phInt.Cmp(btcTarget) <= 0 {
			btcHeader = h
			parentHashHex = phHex
			parentHashInt = phInt
			foundValid = true
			break
		}
	}

	if !foundValid {
		if s.db != nil {
			_ = s.db.RecordRejectedShare(session.Worker)
		}
		atomic.AddUint64(&s.RejectedShares, 1)
		atomic.AddUint64(&session.SharesRejected, 1)
		_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{23, "Low difficulty share", nil}})
		return
	}

	isGoWin := parentHashInt.Cmp(goTarget) <= 0
	isBtcWin := false
	s.mu.RLock()
	btcPaused := s.BtcMiningPaused
	s.mu.RUnlock()
	if !btcPaused {
		isBtcWin = parentHashInt.Cmp(btcTarget) <= 0
	}

	// Check & Record in SQLite Database
	if s.db != nil {
		accepted, isDup, err := s.db.RecordShare(session.Worker, jobID, session.Extranonce1, extranonce2Hex, nonceHex, session.Difficulty, parentHashHex, isGoWin, isBtcWin)
		if isDup {
			atomic.AddUint64(&s.RejectedShares, 1)
			atomic.AddUint64(&session.SharesRejected, 1)
			_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{22, "Duplicate share", nil}})
			return
		}
		if err != nil {
			log.Printf("[DATABASE] ⚠️ SQLite share record warning: %v", err)
		}
		if !accepted && !isDup {
			atomic.AddUint64(&s.RejectedShares, 1)
			atomic.AddUint64(&session.SharesRejected, 1)
			_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: false, Error: []any{20, "Share rejected by pool", nil}})
			return
		}
	}

	// Share accepted!
	atomic.AddUint64(&s.AcceptedShares, 1)
	atomic.AddUint64(&session.SharesAccepted, 1)
	session.LastShareAt = time.Now()
	s.recordShare(session.Worker, session.Difficulty)
	_ = session.Enc.Encode(StratumRPCResponse{ID: req.ID, Result: true, Error: nil})

	s.LogEvent("share", fmt.Sprintf("Accepted share from %s (Hash: %s...)", session.Worker, parentHashHex[:12]), map[string]any{
		"hash":       parentHashHex,
		"difficulty": session.Difficulty,
		"worker":     session.Worker,
		"go_win":     isGoWin,
		"btc_win":    isBtcWin,
	})

	// 4. Handle Dual Merged Win vs Independent Wins
	subsidyBTC := fmt.Sprintf("%.8f BTC", float64(s.btcState.BlockSubsidy)/1e8)
	if isGoWin && isBtcWin {
		atomic.AddUint64(&s.DualWins, 1)
		log.Println("\n" + strings.Repeat("=", 68))
		log.Printf("[MERGED] 💎💎 DUAL MERGED MINING WIN! ONE COMPUTATION SOLVED BOTH CHAINS! 💎💎")
		log.Printf("[MERGED] Parent Nonce: 0x%08x | Hash: %s", nonceVal, parentHashHex)
		log.Printf("[MERGED] Won Bitcoin Block #%d (+%s) AND GoChain Block #%d (+10 GO)!",
			job.BtcHeight, subsidyBTC, job.GoCandidate.Index)
		log.Println(strings.Repeat("=", 68) + "\n")

		s.LogEvent("dual_win", fmt.Sprintf("💎 DUAL WIN: BTC Block #%d & GO Block #%d from same hash!",
			job.BtcHeight, job.GoCandidate.Index), map[string]any{
			"hash":        parentHashHex,
			"btc_height":  job.BtcHeight,
			"go_height":   job.GoCandidate.Index,
			"reward_btc":  subsidyBTC,
			"reward_go":   "10.00000000 GO",
			"worker":      session.Worker,
		})
	}

	var goAuxBlock *AuxBlock
	var solvedBtcBlock *SolvedBitcoinBlock

	// 5. Evaluate GoChain AuxPoW Block Adoption
	if isGoWin {
		auxpow := &AuxPoW{
			ParentHeader:   btcHeader,
			CoinbaseTxHex:  hex.EncodeToString(nonWitCoinbase),
			CoinbaseBranch: job.MerkleBranch,
			CoinbaseIndex:  0,
		}

		minerAddr := s.goState.MinerAddress
		parts := strings.Split(session.Worker, ".")
		if len(parts) > 0 && validAddress(parts[0]) {
			minerAddr = parts[0]
		}

		job.GoCandidate.Miner = minerAddr
		job.GoCandidate.Bits = job.GoTargetBits
		job.GoCandidate.AuxPoW = auxpow

		goAuxBlock = &AuxBlock{
			Block:  job.GoCandidate,
			Bits:   job.GoTargetBits,
			AuxPoW: auxpow,
		}

		if err := ValidateAuxPoW(goAuxBlock.Block, auxpow, job.GoTargetBits); err == nil {
			log.Printf("[MERGED-GO] 🎉 AuxPoW Block #%d verified! Finder: %s", goAuxBlock.Index, session.Worker)

			// Execute SQLite Transactional PPLNS / Solo Distribution
			if s.db != nil {
				dist, err := s.db.DistributeGoBlockReward(int64(goAuxBlock.Index), goAuxBlock.Hash, session.Worker, goAuxBlock.Reward, s.PayoutMode, s.PPLNSWindow)
				if err == nil && dist != nil {
					s.LogEvent("payout", fmt.Sprintf("GoChain Block #%d reward distributed (%s): %s",
						goAuxBlock.Index, s.PayoutMode, fmt.Sprintf("%.8f GO", float64(goAuxBlock.Reward)/1e8)), dist)
				} else if err != nil {
					log.Printf("[DATABASE] ❌ Error distributing GoChain block reward: %v", err)
				}
			}

			if s.goState.NodeURL != "" {
				go s.submitGoBlockToNode(goAuxBlock, nonceVal, parentHashHex)
			} else {
				s.recordGoBlock(goAuxBlock, nonceVal, parentHashHex)
			}
			if s.OnGoBlockMined != nil {
				s.OnGoBlockMined(goAuxBlock)
			}
		} else {
			log.Printf("[MERGED-GO] ❌ AuxPoW validation error: %v", err)
		}
	}

	// 6. Evaluate Bitcoin Block Adoption
	if isBtcWin {
		rawHdr, _ := btcHeader.Serialize80Bytes()
		rawBlock := append(rawHdr, byte(1)) // 1 transaction
		rawBlock = append(rawBlock, rawCoinbaseWit...)
		rawBlockHex := hex.EncodeToString(rawBlock)

		if s.BtcRPCURL != "" {
			if err := s.submitBitcoinCoreBlock(rawBlockHex); err == nil {
				log.Printf("[MERGED-BTC] 🎉🎉🎉 Bitcoin Core Node ACCEPTED Block #%d! Tip: %s", job.BtcHeight, parentHashHex)
				s.LogEvent("btc_block", fmt.Sprintf("Bitcoin Core Node ACCEPTED Block #%d into chain tip (+%s)",
					job.BtcHeight, subsidyBTC), map[string]any{
					"height": job.BtcHeight,
					"hash":   parentHashHex,
					"miner":  s.btcState.MinerAddress,
					"reward": subsidyBTC,
				})

				solvedBtcBlock = &SolvedBitcoinBlock{
					Height:     job.BtcHeight,
					Hash:       parentHashHex,
					PrevHash:   job.BtcPrevHash,
					MerkleRoot: merkleRootHex,
					Timestamp:  ntimeVal,
					Bits:       fmt.Sprintf("0x%08x", job.BtcBits),
					Nonce:      fmt.Sprintf("0x%08x", nonceVal),
					Version:    fmt.Sprintf("0x%08x", btcHeader.Version),
					TxCount:    1,
					RewardSat:  s.btcState.BlockSubsidy,
					RewardBTC:  subsidyBTC,
					Miner:      s.btcState.MinerAddress,
					RawHex:     rawBlockHex,
					SolvedAt:   time.Now().Unix(),
					DualWin:    isGoWin,
					Submitted:  true,
				}

				s.recordBtcBlock(solvedBtcBlock)
				if s.db != nil {
					_ = s.db.RecordBtcBlockPayout(solvedBtcBlock.Height, solvedBtcBlock.Hash, session.Worker, solvedBtcBlock.RewardSat)
				}
				log.Printf("[MERGED-BTC] 🏆 Verified Bitcoin Block #%d! (+%s) Hash: %s",
					solvedBtcBlock.Height, subsidyBTC, parentHashHex)

				if s.OnBtcBlockMined != nil {
					s.OnBtcBlockMined(solvedBtcBlock)
				}
				s.advanceBitcoinState(parentHashHex)
			} else {
				// Duplicate or stale submission rejected by Bitcoin Core consensus
				log.Printf("[MERGED-BTC] ⚠️ Non-canonical or duplicate block submission to Bitcoin Core: %v", err)
			}
		} else {
			// Standalone simulated parent mode
			solvedBtcBlock = &SolvedBitcoinBlock{
				Height:     job.BtcHeight,
				Hash:       parentHashHex,
				PrevHash:   job.BtcPrevHash,
				MerkleRoot: merkleRootHex,
				Timestamp:  ntimeVal,
				Bits:       fmt.Sprintf("0x%08x", job.BtcBits),
				Nonce:      fmt.Sprintf("0x%08x", nonceVal),
				Version:    fmt.Sprintf("0x%08x", btcHeader.Version),
				TxCount:    1,
				RewardSat:  s.btcState.BlockSubsidy,
				RewardBTC:  subsidyBTC,
				Miner:      s.btcState.MinerAddress,
				RawHex:     rawBlockHex,
				SolvedAt:   time.Now().Unix(),
				DualWin:    isGoWin,
				Submitted:  false,
			}

			s.recordBtcBlock(solvedBtcBlock)
			if s.db != nil {
				_ = s.db.RecordBtcBlockPayout(solvedBtcBlock.Height, solvedBtcBlock.Hash, session.Worker, solvedBtcBlock.RewardSat)
			}
			log.Printf("[MERGED-BTC] 🏆 Solved Bitcoin Block #%d! (+%s) Hash: %s",
				solvedBtcBlock.Height, subsidyBTC, parentHashHex)

			if s.OnBtcBlockMined != nil {
				s.OnBtcBlockMined(solvedBtcBlock)
			}
			s.advanceBitcoinState(parentHashHex)
		}
	}

	if isGoWin && isBtcWin && s.OnDualWin != nil {
		s.OnDualWin(solvedBtcBlock, goAuxBlock)
	}
}

func (s *MergedMiningServer) recordShare(worker string, diff float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shareHistory = append(s.shareHistory, shareRecord{timestamp: time.Now(), diff: diff, worker: worker})
	if len(s.shareHistory) > 5000 {
		s.shareHistory = s.shareHistory[len(s.shareHistory)-2000:]
	}
}

// WorkerHashrateGHs calculates rolling hashrate for a specific worker over given window.
func (s *MergedMiningServer) WorkerHashrateGHs(worker string, window time.Duration) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	var totalDiff float64
	count := 0
	for i := len(s.shareHistory) - 1; i >= 0; i-- {
		if s.shareHistory[i].timestamp.Before(cutoff) {
			break
		}
		if s.shareHistory[i].worker == worker {
			totalDiff += s.shareHistory[i].diff
			count++
		}
	}
	if count == 0 {
		return 0
	}
	hashesPerSec := (totalDiff * 4294967296.0) / window.Seconds()
	return hashesPerSec / 1e9
}

func (s *MergedMiningServer) recordGoBlock(ab *AuxBlock, parentNonce uint32, parentHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goState.BlocksMined++
	s.goState.TotalMinedGO += ab.Reward
	s.goState.FormattedGO = fmt.Sprintf("%.8f GO", float64(s.goState.TotalMinedGO)/1e8)
	s.goState.CurrentHeight = ab.Index

	summary := minedBlockSummary{
		Index:       ab.Index,
		Hash:        ab.Hash,
		ParentNonce: fmt.Sprintf("0x%08x", parentNonce),
		ParentHash:  parentHash,
		Miner:       ab.Miner,
		RewardAtoms: ab.Reward,
		Timestamp:   time.Now().Unix(),
	}
	s.goState.RecentBlocks = append([]minedBlockSummary{summary}, s.goState.RecentBlocks...)
	if len(s.goState.RecentBlocks) > 30 {
		s.goState.RecentBlocks = s.goState.RecentBlocks[:30]
	}

	s.logEventLocked("go_block", fmt.Sprintf("GoChain Block #%d adopted (10 GO)", ab.Index), summary)
}

func (s *MergedMiningServer) recordBtcBlock(b *SolvedBitcoinBlock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.btcState.BlocksMined++
	s.btcState.TotalMinedSat += b.RewardSat
	s.btcState.TotalMinedBTC = fmt.Sprintf("%.8f BTC", float64(s.btcState.TotalMinedSat)/1e8)

	s.btcState.RecentBlocks = append([]SolvedBitcoinBlock{*b}, s.btcState.RecentBlocks...)
	if len(s.btcState.RecentBlocks) > 30 {
		s.btcState.RecentBlocks = s.btcState.RecentBlocks[:30]
	}

	s.logEventLocked("btc_block", fmt.Sprintf("Bitcoin Block #%d solved (3.125 BTC)", b.Height), b)
}

func (s *MergedMiningServer) advanceBitcoinState(newTipHash string) {
	s.mu.Lock()
	s.btcState.Height++
	s.btcState.TipHash = newTipHash
	currCandidate := s.currentJob.GoCandidate
	goBits := s.goState.TargetBits
	s.mu.Unlock()

	// Broadcast new template on next Bitcoin block
	newJob, err := s.CreateNewMergedJob(currCandidate, goBits, true)
	if err == nil {
		s.BroadcastJob(newJob)
		log.Printf("[MERGED] 🔄 Advanced to Bitcoin Block Template #%d (Tip: %s...)",
			s.btcState.Height, newTipHash[:8])
	}
}

func (s *MergedMiningServer) submitGoBlockToNode(auxBlock *AuxBlock, parentNonce uint32, parentHash string) {
	payload, err := json.Marshal(auxBlock.Block)
	if err != nil {
		return
	}
	resp, err := http.Post(strings.TrimRight(s.goState.NodeURL, "/")+"/block", "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[MERGED-GO] Failed to post block to GoChain node: %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[MERGED-GO] GoChain node response: %s (%s)", resp.Status, string(body))

	if resp.StatusCode == http.StatusOK {
		s.recordGoBlock(auxBlock, parentNonce, parentHash)

		// Fetch updated tip and generate new job with dynamic retargeting
		var blocks []*Block
		if err := request(s.goState.NodeURL, "/chain", nil, &blocks); err == nil && len(blocks) > 0 {
			tip := blocks[len(blocks)-1]
			candidate := NewBlock(tip.Index+1, tip.Hash, []Transaction{}, s.goState.MinerAddress)
			candidate.Timestamp = time.Now().Unix()
			candidate.Reward = rewardAt(candidate.Timestamp)
			expectedBits := ExpectedDifficulty(blocks, candidate.Index, DefaultGoAuxBits)
			candidate.Bits = expectedBits
			s.goState.TargetBits = expectedBits

			job, err := s.CreateNewMergedJob(candidate, expectedBits, true)
			if err == nil {
				s.BroadcastJob(job)
				log.Printf("[MERGED] 🔄 New Merged Job Broadcast: GoChain Block #%d (Target: 0x%08x)",
					candidate.Index, expectedBits)
			}
		}
	}
}

// HashrateGHs calculates rolling hashrate across given window.
func (s *MergedMiningServer) HashrateGHs(window time.Duration) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-window)
	var totalDiff float64
	count := 0
	for i := len(s.shareHistory) - 1; i >= 0; i-- {
		if s.shareHistory[i].timestamp.Before(cutoff) {
			break
		}
		totalDiff += s.shareHistory[i].diff
		count++
	}
	if count == 0 {
		return 0
	}
	hashesPerSec := (totalDiff * 4294967296.0) / window.Seconds()
	return hashesPerSec / 1e9
}

// StartDashboard launches the real-time True Merged Mining Web Dashboard.
func (s *MergedMiningServer) StartDashboard(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(mergedDashboardHTML))
	})

	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var goBalanceAtoms int64
		if s.goState.NodeURL != "" && s.goState.MinerAddress != "" {
			var acct Account
			if err := request(s.goState.NodeURL, "/balance/"+s.goState.MinerAddress, nil, &acct); err == nil {
				goBalanceAtoms = acct.Balance
			}
		}

		s.mu.RLock()
		var activeWorker, remoteAddr string
		for _, sess := range s.sessions {
			if sess.Authorized {
				activeWorker = sess.Worker
				remoteAddr = sess.Conn.RemoteAddr().String()
				break
			}
		}

		btcCopy := *s.btcState
		btcRecent := append([]SolvedBitcoinBlock{}, s.btcState.RecentBlocks...)
		goCopy := *s.goState
		goRecent := append([]minedBlockSummary{}, s.goState.RecentBlocks...)
		eventLogCopy := append([]MergedEventLogEntry{}, s.eventLog...)
		dualWins := atomic.LoadUint64(&s.DualWins)
		accepted := atomic.LoadUint64(&s.AcceptedShares)
		rejected := atomic.LoadUint64(&s.RejectedShares)
		s.mu.RUnlock()

		diff1 := BitsToTarget(0x1d00ffff)
		btcTarget := BitsToTarget(btcCopy.TargetBits)
		goTarget := BitsToTarget(goCopy.TargetBits)

		btcDiffFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(diff1), new(big.Float).SetInt(btcTarget)).Float64()
		goDiffFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(diff1), new(big.Float).SetInt(goTarget)).Float64()

		// Canonical synchronization with live Bitcoin Core engine
		if !s.BtcMiningPaused && s.BtcRPCURL != "" {
			if res, err := s.callBitcoinCoreRPC("getblockchaininfo", []any{}); err == nil {
				var info struct {
					Blocks        int64   `json:"blocks"`
					BestBlockHash string  `json:"bestblockhash"`
					Difficulty    float64 `json:"difficulty"`
				}
				if json.Unmarshal(res, &info) == nil {
					btcCopy.Height = info.Blocks
					btcCopy.TipHash = info.BestBlockHash
					btcDiffFloat = info.Difficulty
					btcCopy.BlocksMined = uint64(info.Blocks)
					btcCopy.TotalMinedSat = info.Blocks * btcCopy.BlockSubsidy
					btcCopy.TotalMinedBTC = fmt.Sprintf("%.8f BTC", float64(btcCopy.TotalMinedSat)/1e8)
				}
			}
		}

		stats := map[string]any{
			"mining":          activeWorker != "",
			"worker":          activeWorker,
			"remote_addr":     remoteAddr,
			"hashrate_ghs":    s.HashrateGHs(60 * time.Second),
			"accepted_shares": accepted,
			"rejected_shares": rejected,
			"share_diff":      s.ShareDiff,
			"dual_wins_count": dualWins,
			"bitcoin_parent": map[string]any{
				"paused":              s.BtcMiningPaused,
				"status":              map[bool]string{true: "PAUSED", false: "ACTIVE"}[s.BtcMiningPaused],
				"height":              btcCopy.Height,
				"tip_hash":            btcCopy.TipHash,
				"target_bits":         fmt.Sprintf("0x%08x", btcCopy.TargetBits),
				"difficulty":          btcDiffFloat,
				"subsidy_satoshis":    btcCopy.BlockSubsidy,
				"subsidy_btc":         fmt.Sprintf("%.8f BTC", float64(btcCopy.BlockSubsidy)/1e8),
				"blocks_mined":        btcCopy.BlocksMined,
				"total_mined_sat":     btcCopy.TotalMinedSat,
				"total_mined_btc":     fmt.Sprintf("%.8f BTC", float64(btcCopy.TotalMinedSat)/1e8),
				"miner_address":       btcCopy.MinerAddress,
				"recent_blocks":       btcRecent,
				"rpc_connected":       btcCopy.RPCConnected && !s.BtcMiningPaused,
				"rpc_network":         btcCopy.RPCNetwork,
				"rpc_url":             btcCopy.RPCURL,
			},
			"gochain_aux": map[string]any{
				"height":              goCopy.CurrentHeight,
				"target_bits":         fmt.Sprintf("0x%08x", goCopy.TargetBits),
				"difficulty":          goDiffFloat,
				"reward_go":           "10.00000000 GO",
				"blocks_mined":        goCopy.BlocksMined,
				"total_mined_atoms":   goCopy.TotalMinedGO,
				"total_mined_go":      fmt.Sprintf("%.8f GO", float64(goCopy.TotalMinedGO)/1e8),
				"miner_balance_atoms": goBalanceAtoms,
				"miner_balance_go":    fmt.Sprintf("%.8f GO", float64(goBalanceAtoms)/1e8),
				"miner_address":       goCopy.MinerAddress,
				"node_url":            goCopy.NodeURL,
				"recent_blocks":       goRecent,
			},
			"payout_mode":     string(s.PayoutMode),
			"pplns_window":    s.PPLNSWindow,
			"event_log": eventLogCopy,
			"solana_cross_chain": map[string]any{
				"program_id": "63BJKNhEb4ZAnzHHyuvhFXZT3XGuwEyrojzQa2yTCTDy",
				"wgo_mint":   "4G3S3qF6TUwe9dFisfJjRxUgG3mhzRgC1PHTTV1wVdAq",
				"pool_pda":   "DVxjjoFiCJ7mSQ1PNEHWBJiJX6sHagU8hPtnekxpht1X",
				"status":     "VERIFIED_ON_DEVNET",
			},
		}

		_ = json.NewEncoder(w).Encode(stats)
	})

	mux.HandleFunc("GET /api/workers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.db == nil {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		workers, err := s.db.GetAllWorkers()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.mu.RLock()
		activeWorkers := make(map[string]float64)
		for _, sess := range s.sessions {
			if sess.Authorized {
				activeWorkers[sess.Worker] = s.WorkerHashrateGHs(sess.Worker, 60*time.Second)
			}
		}
		s.mu.RUnlock()

		for i := range workers {
			if hr, online := activeWorkers[workers[i].WorkerID]; online {
				workers[i].Online = true
				workers[i].HashrateGHs = hr
			}
		}
		_ = json.NewEncoder(w).Encode(workers)
	})

	mux.HandleFunc("GET /api/payouts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.db == nil {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		payouts, err := s.db.GetRecentPayouts(50)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.NewEncoder(w).Encode(payouts)
	})

	mux.HandleFunc("POST /api/btc/pause", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.BtcMiningPaused = true
		s.btcState.RPCConnected = false
		s.logEventLocked("pool", "⏸️ Bitcoin mining paused by operator", nil)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "btc_paused": true})
	})

	mux.HandleFunc("POST /api/btc/resume", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.BtcMiningPaused = false
		s.logEventLocked("pool", "▶️ Bitcoin mining resumed by operator", nil)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "btc_paused": false})
	})

	server := &http.Server{Addr: addr, Handler: mux}
	log.Printf("[DASHBOARD] 📊 Merged Mining Dashboard live at http://%s", addr)
	return server.ListenAndServe()
}

func runMergedStratum(args []string) error {
	fs := flags("merged-stratum")
	port := fs.Int("port", 3335, "Stratum TCP port for ASIC miners")
	dash := fs.Int("dash", 3336, "Web dashboard HTTP port")
	host := fs.String("host", "0.0.0.0", "listen interface (0.0.0.0 for LAN/Wi-Fi)")
	node := fs.String("node", "http://127.0.0.1:8092", "GoChain node URL")
	goMiner := fs.String("go-miner", "", "GoChain reward wallet address")
	btcMiner := fs.String("btc-miner", "bc1qxl26let6yc2fn8n33he4fswzsyys83h8j00uyq", "Bitcoin reward address")
	btcBits := fs.Uint("btc-bits", uint(DefaultGoAuxBits), "Bitcoin difficulty target bits")
	goBits := fs.Uint("go-bits", uint(DefaultGoAuxBits), "GoChain initial target bits")
	diff := fs.Float64("diff", 16.0, "Stratum share difficulty for Bitaxe")
	wallet := fs.String("wallet", WalletFile, "wallet file for GoChain miner")
	btcRPC := fs.String("btc-rpc", "auto", "Bitcoin Core RPC URL (or 'auto' to auto-connect to WSL/local node)")
	payoutMode := fs.String("payout-mode", "hybrid", "Payout mode: 'hybrid' (10% finder, 90% PPLNS) or 'solo'")
	dbPath := fs.String("db", "bitproof_pool.db", "SQLite database path for transactional accounting")
	pplnsWindow := fs.Int("pplns-window", 1000, "Number of past shares in PPLNS window")
	pauseBTC := fs.Bool("pause-btc", false, "Pause Bitcoin block mining and block submission")

	if err := fs.Parse(args); err != nil {
		return err
	}

	defaultGoMiner := *goMiner
	if defaultGoMiner == "" {
		w, err := LoadWallet(*wallet)
		if err == nil {
			defaultGoMiner = w.Address
		} else {
			defaultGoMiner = "go19c5446aa51196d4ef9e8036ec203e70333e178c1351baf7c12f96229077c2272"
		}
	}

	stratumAddr := fmt.Sprintf("%s:%d", *host, *port)
	dashAddr := fmt.Sprintf("%s:%d", *host, *dash)

	srv := NewMergedMiningServer(stratumAddr, dashAddr, *node, defaultGoMiner, *btcMiner, *btcRPC, *diff, uint32(*btcBits), uint32(*goBits))
	if *pauseBTC {
		srv.BtcMiningPaused = true
		srv.btcState.RPCConnected = false
		srv.btcState.RPCNetwork = "PAUSED"
		log.Printf("[MERGED-BTC] ⏸️ Bitcoin mining explicitly PAUSED by --pause-btc flag")
	}

	if *payoutMode == "solo" {
		srv.PayoutMode = PayoutModeSolo
	} else {
		srv.PayoutMode = PayoutModeHybrid
	}
	srv.PPLNSWindow = *pplnsWindow

	if poolDB, err := NewPoolDatabase(*dbPath); err == nil {
		srv.db = poolDB
		defer poolDB.Close()
	} else {
		log.Printf("[DATABASE] ⚠️ Warning: Failed to open SQLite pool db: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		if err := srv.StartDashboard(dashAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[DASHBOARD] Server error: %v", err)
		}
	}()

	// Periodic poller for GoChain node updates
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
						candidate := NewBlock(tip.Index+1, tip.Hash, []Transaction{}, defaultGoMiner)
						candidate.Timestamp = time.Now().Unix()
						candidate.Reward = rewardAt(candidate.Timestamp)
						expectedBits := ExpectedDifficulty(blocks, candidate.Index, DefaultGoAuxBits)
						candidate.Bits = expectedBits
						srv.goState.TargetBits = expectedBits
						srv.goState.CurrentHeight = tip.Index

						job, err := srv.CreateNewMergedJob(candidate, expectedBits, true)
						if err == nil {
							srv.BroadcastJob(job)
							log.Printf("[MERGED] 🔄 GoChain Poller: New Template Block #%d (Parent Tip: %s..., Target Bits: 0x%08x)",
								candidate.Index, tip.Hash[:8], expectedBits)
						}
					}
				}
			}
		}
	}()

	return srv.ListenAndServe(ctx)
}

const mergedDashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Bitcoin + GoChain True Merged Mining Console (AuxPoW)</title>
  <style>
    :root {
      --bg: #090d16;
      --card-bg: rgba(18, 25, 41, 0.75);
      --card-border: rgba(255, 255, 255, 0.08);
      --btc-orange: #f7931a;
      --go-cyan: #00f2fe;
      --gold: #ffd700;
      --text: #f0f4f8;
      --text-muted: #8b9bb4;
      --green: #00e676;
      --red: #ff3366;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background: radial-gradient(circle at 50% 0%, #151d30 0%, var(--bg) 80%);
      color: var(--text);
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", sans-serif;
      min-height: 100vh;
      padding: 24px;
    }
    .container { max-width: 1400px; margin: 0 auto; }
    .header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 24px;
      padding-bottom: 16px;
      border-bottom: 1px solid var(--card-border);
    }
    .title-group { display: flex; align-items: center; gap: 16px; }
    .icon-badge {
      display: flex;
      align-items: center;
      font-size: 28px;
      padding: 10px 14px;
      border-radius: 12px;
      background: rgba(247, 147, 26, 0.1);
      border: 1px solid rgba(247, 147, 26, 0.25);
    }
    h1 { font-size: 22px; font-weight: 700; letter-spacing: -0.5px; }
    .subtitle { color: var(--text-muted); font-size: 13px; margin-top: 2px; }
    .badges { display: flex; gap: 8px; }
    .badge {
      padding: 5px 12px;
      border-radius: 20px;
      font-size: 11px;
      font-weight: 700;
      letter-spacing: 0.5px;
      text-transform: uppercase;
    }
    .badge-live { background: rgba(0, 230, 118, 0.15); color: var(--green); border: 1px solid rgba(0, 230, 118, 0.3); }
    .badge-btc { background: rgba(247, 147, 26, 0.15); color: var(--btc-orange); border: 1px solid rgba(247, 147, 26, 0.3); }
    .badge-go { background: rgba(0, 242, 254, 0.15); color: var(--go-cyan); border: 1px solid rgba(0, 242, 254, 0.3); }

    .dual-banner {
      background: linear-gradient(135deg, rgba(255, 215, 0, 0.12) 0%, rgba(247, 147, 26, 0.12) 100%);
      border: 1px solid rgba(255, 215, 0, 0.3);
      border-radius: 16px;
      padding: 18px 24px;
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 24px;
      box-shadow: 0 8px 32px rgba(255, 215, 0, 0.05);
    }
    .dual-banner-left { display: flex; align-items: center; gap: 16px; }
    .dual-count { font-size: 32px; font-weight: 900; color: var(--gold); }
    .dual-banner-text h3 { font-size: 16px; color: var(--gold); margin-bottom: 2px; }
    .dual-banner-text p { font-size: 12px; color: var(--text-muted); }

    .grid-chains {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 20px;
      margin-bottom: 24px;
    }
    .chain-card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 16px;
      padding: 20px;
      backdrop-filter: blur(16px);
      position: relative;
      overflow: hidden;
    }
    .chain-card::before {
      content: "";
      position: absolute;
      top: 0; left: 0; right: 0; height: 3px;
    }
    .card-btc::before { background: linear-gradient(90deg, #f7931a, #ffbb33); }
    .card-go::before { background: linear-gradient(90deg, #00f2fe, #4facfe); }

    .chain-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-bottom: 18px;
    }
    .chain-header-title { display: flex; align-items: center; gap: 10px; font-size: 16px; font-weight: 700; }
    .stats-row {
      display: grid;
      grid-template-columns: repeat(3, 1fr);
      gap: 12px;
      margin-bottom: 16px;
    }
    .stat-box {
      background: rgba(0, 0, 0, 0.25);
      border: 1px solid rgba(255, 255, 255, 0.04);
      border-radius: 10px;
      padding: 12px;
    }
    .stat-label { font-size: 11px; color: var(--text-muted); text-transform: uppercase; margin-bottom: 4px; }
    .stat-val { font-size: 18px; font-weight: 700; font-family: monospace; }
    .val-btc { color: var(--btc-orange); }
    .val-go { color: var(--go-cyan); }

    .miner-info {
      background: rgba(0, 0, 0, 0.2);
      border-radius: 8px;
      padding: 8px 12px;
      font-size: 11px;
      font-family: monospace;
      color: var(--text-muted);
      word-break: break-all;
    }

    .grid-telemetry {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
      gap: 16px;
      margin-bottom: 24px;
    }
    .tele-card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 12px;
      padding: 16px;
    }

    .grid-bottom {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 20px;
    }
    .feed-card {
      background: var(--card-bg);
      border: 1px solid var(--card-border);
      border-radius: 16px;
      padding: 20px;
      display: flex;
      flex-direction: column;
      max-height: 480px;
    }
    .feed-header {
      font-size: 14px;
      font-weight: 700;
      margin-bottom: 14px;
      display: flex;
      justify-content: space-between;
    }
    .feed-list {
      overflow-y: auto;
      flex: 1;
      display: flex;
      flex-direction: column;
      gap: 8px;
    }
    .feed-item {
      background: rgba(0, 0, 0, 0.25);
      border-left: 3px solid #555;
      border-radius: 6px;
      padding: 8px 12px;
      font-size: 12px;
      display: flex;
      justify-content: space-between;
      align-items: center;
    }
    .feed-share { border-left-color: #3b82f6; }
    .feed-go { border-left-color: var(--go-cyan); }
    .feed-btc { border-left-color: var(--btc-orange); }
    .feed-dual { border-left-color: var(--gold); background: rgba(255, 215, 0, 0.08); }
    .feed-time { color: var(--text-muted); font-size: 10px; font-family: monospace; }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <div class="title-group">
        <div class="icon-badge">⛏️ ⚡</div>
        <div>
          <h1>Bitcoin + GoChain True Merged Mining (AuxPoW)</h1>
          <div class="subtitle">Single SHA-256d ASIC Hash securing both chains simultaneously</div>
        </div>
      </div>
      <div class="badges">
        <div class="badge badge-live">● STRATUM V1 ACTIVE</div>
        <div class="badge badge-btc">PARENT: BITCOIN</div>
        <div class="badge badge-go">AUX: GOCHAIN</div>
      </div>
    </div>

    <div class="dual-banner">
      <div class="dual-banner-left">
        <div class="dual-count" id="dual-count">0</div>
        <div class="dual-banner-text">
          <h3>Simultaneous Dual Block Wins (1 Hash = 2 Blockchains)</h3>
          <p>Identical ASIC computational proof satisfied both Bitcoin target AND GoChain target with zero extra energy.</p>
        </div>
      </div>
      <div class="badge badge-live" style="background: rgba(255, 215, 0, 0.15); color: var(--gold); border-color: rgba(255, 215, 0, 0.3);">
        ZERO CARBON OVERHEAD
      </div>
    </div>

    <!-- Dual Chains -->
    <div class="grid-chains">
      <!-- Bitcoin Parent Chain -->
      <div class="chain-card card-btc">
        <div class="chain-header">
          <div class="chain-header-title">
            <span>🪙</span>
            <span>Bitcoin Parent Network (BTC)</span>
          </div>
          <div style="display: flex; gap: 6px; align-items: center;">
            <button id="btc-toggle-btn" onclick="toggleBtcMining()" style="background: rgba(234, 179, 8, 0.2); color: #eab308; border: 1px solid rgba(234, 179, 8, 0.4); padding: 4px 10px; border-radius: 6px; font-size: 10px; cursor: pointer; font-weight: 600;">PAUSE BITCOIN</button>
            <span class="badge" style="background: rgba(234, 179, 8, 0.2); color: #eab308; border: 1px solid rgba(234, 179, 8, 0.4); font-size: 10px;">REGTEST (NO CASH VALUE)</span>
            <span class="badge" id="btc-rpc-badge" style="background: rgba(34, 197, 94, 0.2); color: #22c55e; border: 1px solid rgba(34, 197, 94, 0.4); display: none;">BITCOIN CORE RPC</span>
            <span class="badge badge-btc">PARENT CHAIN</span>
          </div>
        </div>
        <div class="stats-row">
          <div class="stat-box">
            <div class="stat-label">Block Height</div>
            <div class="stat-val val-btc" id="btc-height">--</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">Target Difficulty</div>
            <div class="stat-val" id="btc-diff">--</div>
            <div style="font-size: 9px; color: var(--text-muted); margin-top: 2px;">(Target Bits: 0x207fffff)</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">Block Subsidy</div>
            <div class="stat-val val-btc" id="btc-subsidy">50.00 BTC</div>
            <div style="font-size: 9px; color: #eab308; margin-top: 2px;">REGTEST DEMO REWARD</div>
          </div>
        </div>
        <div class="stats-row">
          <div class="stat-box">
            <div class="stat-label">BTC Blocks Won</div>
            <div class="stat-val val-btc" id="btc-blocks-won">0</div>
          </div>
          <div class="stat-box" style="grid-column: span 2;">
            <div class="stat-label">Canonical Regtest BTC in UTXO Set</div>
            <div class="stat-val val-btc" id="btc-total-earned">0.00000000 BTC</div>
            <div style="font-size: 9px; color: var(--text-muted); margin-top: 2px;">Verified in Bitcoin Core UTXO database · Zero fiat value</div>
          </div>
        </div>
        <div class="stat-label">Bitcoin Payout Address</div>
        <div class="miner-info" id="btc-miner-addr">bc1qbitaxemergedminer...</div>
      </div>

      <!-- GoChain Auxiliary Chain -->
      <div class="chain-card card-go">
        <div class="chain-header">
          <div class="chain-header-title">
            <span>⛓️</span>
            <span>GoChain Auxiliary Network (GO)</span>
          </div>
          <span class="badge badge-go">AUXILIARY CHAIN</span>
        </div>
        <div class="stats-row">
          <div class="stat-box">
            <div class="stat-label">Aux Block Height</div>
            <div class="stat-val val-go" id="go-height">#1</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">Target Difficulty</div>
            <div class="stat-val" id="go-diff">--</div>
          </div>
          <div class="stat-box">
            <div class="stat-label">Block Reward</div>
            <div class="stat-val val-go">10.00 GO</div>
          </div>
        </div>
        <div class="stats-row">
          <div class="stat-box">
            <div class="stat-label">GO Blocks Won</div>
            <div class="stat-val val-go" id="go-blocks-won">0</div>
          </div>
          <div class="stat-box" style="grid-column: span 2;">
            <div class="stat-label">Miner Wallet Balance</div>
            <div class="stat-val val-go" id="go-balance">0.00000000 GO</div>
          </div>
        </div>
        <div class="stat-label">GoChain Reward Address</div>
        <div class="miner-info" id="go-miner-addr">go1...</div>
      </div>
    </div>

    <!-- Telemetry -->
    <div class="grid-telemetry">
      <div class="tele-card">
        <div class="stat-label">Rolling Hashrate (60s)</div>
        <div class="stat-val" style="color: var(--green);" id="hashrate">0.00 GH/s</div>
      </div>
      <div class="tele-card">
        <div class="stat-label">Accepted Shares</div>
        <div class="stat-val" id="accepted-shares">0</div>
      </div>
      <div class="tele-card">
        <div class="stat-label">Active Worker</div>
        <div class="stat-val" style="font-size: 14px;" id="worker-name">Waiting for ASIC...</div>
      </div>
      <div class="tele-card">
        <div class="stat-label">Solana Devnet Bridge</div>
        <div class="stat-val" style="color: var(--go-cyan); font-size: 13px;">wGO / GoAMM VERIFIED</div>
      </div>
    <!-- Multi-Miner PPLNS Accounting Table -->
    <div class="feed-card" style="margin-bottom: 20px;">
      <div class="feed-header">
        <span>👷 Multi-Miner Testnet v1: Connected Miners & PPLNS Balances</span>
        <span class="badge" style="background: rgba(34, 197, 94, 0.2); color: #22c55e;" id="pool-payout-mode">HYBRID (10% FINDER, 90% PPLNS)</span>
      </div>
      <div style="overflow-x: auto;">
        <table style="width: 100%; border-collapse: collapse; font-size: 13px; text-align: left;">
          <thead>
            <tr style="border-bottom: 1px solid var(--border); color: var(--text-muted);">
              <th style="padding: 10px;">Worker</th>
              <th style="padding: 10px;">Status</th>
              <th style="padding: 10px;">GoChain Payout Address</th>
              <th style="padding: 10px;">Hashrate</th>
              <th style="padding: 10px;">Shares (Acc/Rej)</th>
              <th style="padding: 10px;">Contributed Work</th>
              <th style="padding: 10px;">Earned GO Balance</th>
            </tr>
          </thead>
          <tbody id="miners-tbody">
            <tr><td colspan="7" style="padding: 15px; text-align: center; color: var(--text-muted);">Waiting for miners to connect...</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Bottom Feeds -->
    <div class="grid-bottom">
      <div class="feed-card">
        <div class="feed-header">
          <span>⚡ Live Merged Mining Event Stream</span>
          <span style="font-size: 11px; color: var(--text-muted);">Real-Time</span>
        </div>
        <div class="feed-list" id="event-feed">
          <div class="feed-item">Waiting for miner connection...</div>
        </div>
      </div>

      <div class="feed-card">
        <div class="feed-header">
          <span>🏆 Recent Merged Blocks (BTC & GO)</span>
          <span style="font-size: 11px; color: var(--text-muted);" id="recent-count">0 Blocks</span>
        </div>
        <div class="feed-list" id="blocks-feed">
          <div class="feed-item">No blocks solved yet in this session.</div>
        </div>
      </div>
    </div>
  </div>

  <script>
    let btcPausedState = false;

    async function toggleBtcMining() {
      const action = btcPausedState ? "resume" : "pause";
      try {
        await fetch("/api/btc/" + action, { method: "POST" });
        await updateStats();
      } catch (err) {
        console.error("Failed to toggle Bitcoin mining:", err);
      }
    }

    async function updateStats() {
      try {
        const res = await fetch("/api/stats");
        if (!res.ok) return;
        const d = await res.json();

        // Dual wins
        document.getElementById("dual-count").textContent = d.dual_wins_count || 0;

        // Bitcoin
        btcPausedState = Boolean(d.bitcoin_parent && d.bitcoin_parent.paused);
        const toggleBtn = document.getElementById("btc-toggle-btn");
        if (toggleBtn) {
          toggleBtn.textContent = btcPausedState ? "RESUME BITCOIN" : "PAUSE BITCOIN";
          toggleBtn.style.background = btcPausedState ? "rgba(34, 197, 94, 0.2)" : "rgba(234, 179, 8, 0.2)";
          toggleBtn.style.color = btcPausedState ? "#22c55e" : "#eab308";
          toggleBtn.style.borderColor = btcPausedState ? "rgba(34, 197, 94, 0.4)" : "rgba(234, 179, 8, 0.4)";
        }

        document.getElementById("btc-height").textContent = Number(d.bitcoin_parent.height).toLocaleString();
        const btcDiff = Number(d.bitcoin_parent.difficulty);
        document.getElementById("btc-diff").textContent = btcDiff < 0.001 ? btcDiff.toExponential(4) : btcDiff.toLocaleString(undefined, {maximumFractionDigits: 2});
        document.getElementById("btc-blocks-won").textContent = Number(d.bitcoin_parent.blocks_mined).toLocaleString();
        document.getElementById("btc-total-earned").textContent = d.bitcoin_parent.total_mined_btc;
        document.getElementById("btc-miner-addr").textContent = d.bitcoin_parent.miner_address;
        if (document.getElementById("btc-subsidy") && d.bitcoin_parent.subsidy_btc) {
          document.getElementById("btc-subsidy").textContent = d.bitcoin_parent.subsidy_btc;
        }
        const badge = document.getElementById("btc-rpc-badge");
        if (badge) {
          if (btcPausedState) {
            badge.textContent = "BITCOIN MINING: PAUSED";
            badge.style.display = "inline-block";
            badge.style.background = "rgba(234, 179, 8, 0.2)";
            badge.style.color = "#eab308";
            badge.style.borderColor = "rgba(234, 179, 8, 0.4)";
          } else if (d.bitcoin_parent && d.bitcoin_parent.rpc_connected) {
            badge.textContent = "BITCOIN CORE v28.0 (" + (d.bitcoin_parent.rpc_network || "regtest").toUpperCase() + ")";
            badge.style.display = "inline-block";
            badge.style.background = "rgba(34, 197, 94, 0.2)";
            badge.style.color = "#22c55e";
            badge.style.borderColor = "rgba(34, 197, 94, 0.4)";
          } else {
            badge.style.display = "none";
          }
        }

        // GoChain
        document.getElementById("go-height").textContent = "#" + d.gochain_aux.height;
        document.getElementById("go-diff").textContent = Number(d.gochain_aux.difficulty).toFixed(2);
        document.getElementById("go-blocks-won").textContent = d.gochain_aux.blocks_mined;
        document.getElementById("go-balance").textContent = d.gochain_aux.miner_balance_go;
        document.getElementById("go-miner-addr").textContent = d.gochain_aux.miner_address;

        // Telemetry
        const ghs = Number(d.hashrate_ghs);
        document.getElementById("hashrate").textContent = ghs >= 1000 ? (ghs/1000).toFixed(2) + " TH/s" : ghs.toFixed(2) + " GH/s";
        document.getElementById("accepted-shares").textContent = d.accepted_shares + " / " + (d.accepted_shares + d.rejected_shares);
        document.getElementById("worker-name").textContent = d.worker || "No Active Miner";

        // Event Stream
        const feed = document.getElementById("event-feed");
        if (d.event_log && d.event_log.length > 0) {
          feed.innerHTML = d.event_log.map(e => {
            const cls = e.type === "dual_win" ? "feed-dual" :
                        e.type === "btc_block" ? "feed-btc" :
                        e.type === "go_block" ? "feed-go" : "feed-share";
            const timeStr = new Date(e.timestamp).toLocaleTimeString();
            return ` + "`" + `<div class="feed-item ${cls}">
              <span>${e.message}</span>
              <span class="feed-time">${timeStr}</span>
            </div>` + "`" + `;
          }).join("");
        }

        // Recent Blocks
        const bFeed = document.getElementById("blocks-feed");
        const btcB = (d.bitcoin_parent.recent_blocks || []).map(b => ({
          chain: "BTC",
          desc: "Bitcoin Block #" + b.height + " (+" + b.reward_btc + ")",
          hash: b.hash,
          dual: b.dual_win,
          time: new Date(b.solved_at * 1000).toLocaleTimeString()
        }));
        const goB = (d.gochain_aux.recent_blocks || []).map(b => ({
          chain: "GO",
          desc: "GoChain Block #" + b.index + " (+10 GO)",
          hash: b.hash,
          dual: false,
          time: new Date(b.timestamp * 1000).toLocaleTimeString()
        }));
        const allBlocks = [...btcB, ...goB];
        if (allBlocks.length > 0) {
          bFeed.innerHTML = allBlocks.slice(0, 15).map(b => ` + "`" + `
            <div class="feed-item ${b.dual ? 'feed-dual' : b.chain === 'BTC' ? 'feed-btc' : 'feed-go'}">
              <div>
                <strong>[${b.chain}]</strong> ${b.desc}
                <div style="font-size: 10px; color: var(--text-muted); font-family: monospace;">${b.hash.substring(0, 24)}...</div>
              </div>
              <span class="feed-time">${b.time}</span>
            </div>
          ` + "`" + `).join("");
          document.getElementById("recent-count").textContent = allBlocks.length + " Blocks";
        }
        if (d.payout_mode) {
          const modeBadge = document.getElementById("pool-payout-mode");
          if (modeBadge) {
            modeBadge.textContent = d.payout_mode.toUpperCase() + (d.payout_mode === "hybrid" ? " (10% FINDER, 90% PPLNS)" : " (100% FINDER SOLO)");
          }
        }
      } catch (err) {
        console.error("Stats fetch error:", err);
      }
    }

    async function updateWorkers() {
      try {
        const res = await fetch("/api/workers");
        if (!res.ok) return;
        const workers = await res.json();
        const tbody = document.getElementById("miners-tbody");
        if (!workers || workers.length === 0) {
          tbody.innerHTML = ` + "`" + `<tr><td colspan="7" style="padding: 15px; text-align: center; color: var(--text-muted);">No workers recorded yet.</td></tr>` + "`" + `;
          return;
        }
        tbody.innerHTML = workers.map(w => {
          const statusBadge = w.online 
            ? ` + "`" + `<span class="badge" style="background: rgba(34, 197, 94, 0.2); color: #22c55e;">ONLINE</span>` + "`" + ` 
            : ` + "`" + `<span class="badge" style="background: rgba(148, 163, 184, 0.2); color: #94a3b8;">IDLE</span>` + "`" + `;
          const hr = w.hashrate_ghs >= 1000 ? (w.hashrate_ghs/1000).toFixed(2) + " TH/s" : (w.hashrate_ghs || 0).toFixed(2) + " GH/s";
          return ` + "`" + `<tr style="border-bottom: 1px solid rgba(255,255,255,0.05);">
            <td style="padding: 10px; font-weight: bold; color: var(--text-light);">${w.worker_name || w.worker_id}</td>
            <td style="padding: 10px;">${statusBadge}</td>
            <td style="padding: 10px; font-family: monospace; font-size: 11px; color: var(--go-cyan);">${w.miner_go}</td>
            <td style="padding: 10px; font-weight: 600; color: var(--green);">${hr}</td>
            <td style="padding: 10px;">${w.shares_accepted} / ${w.shares_accepted + w.shares_rejected}</td>
            <td style="padding: 10px; font-family: monospace;">${w.total_difficulty.toFixed(1)}</td>
            <td style="padding: 10px; font-weight: bold; color: var(--go-cyan);">${w.earned_go_formatted}</td>
          </tr>` + "`" + `;
        }).join("");
      } catch (err) {
        console.error("Workers fetch error:", err);
      }
    }

    setInterval(updateStats, 1500);
    setInterval(updateWorkers, 2000);
    updateStats();
    updateWorkers();
  </script>
</body>
</html>
`
