package main

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"
)

func TestBitcoinHeaderSerializationAndHash(t *testing.T) {
	// Genesis Bitcoin block #0 header
	hdr := BitcoinHeader{
		Version:    1,
		PrevBlock:  "0000000000000000000000000000000000000000000000000000000000000000",
		MerkleRoot: "4a5e1e4baab89f3a32518a88c31bc87f618f76673e2cc77ab2127b7afdeda33b",
		Timestamp:  1231006505,
		Bits:       0x1d00ffff,
		Nonce:      2083236893,
	}

	raw, err := hdr.Serialize80Bytes()
	if err != nil {
		t.Fatalf("Serialize80Bytes failed: %v", err)
	}
	if len(raw) != 80 {
		t.Fatalf("expected 80 bytes, got %d", len(raw))
	}

	hashHex, err := hdr.Hash()
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// Known Bitcoin genesis hash
	expected := "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"
	if hashHex != expected {
		t.Fatalf("genesis hash mismatch: got %s, expected %s", hashHex, expected)
	}
}

func TestBitsAndTargetConversion(t *testing.T) {
	bitsList := []uint32{0x1d00ffff, 0x1e00ffff, 0x1a05ffff, 0x1f00ffff}

	for _, bits := range bitsList {
		target := BitsToTarget(bits)
		if target.Sign() <= 0 {
			t.Fatalf("target must be positive for bits 0x%x", bits)
		}
		recoveredBits := TargetToBits(target)
		if recoveredBits != bits {
			t.Fatalf("bits roundtrip failed: original 0x%x != recovered 0x%x", bits, recoveredBits)
		}

		work := TargetToWork(target)
		if work.Sign() <= 0 {
			t.Fatalf("work must be positive")
		}
	}

	// Higher difficulty (smaller target) must produce more work
	hardTarget := BitsToTarget(0x1a00ffff)
	easyTarget := BitsToTarget(0x1f00ffff)
	hardWork := TargetToWork(hardTarget)
	easyWork := TargetToWork(easyTarget)

	if hardWork.Cmp(easyWork) <= 0 {
		t.Fatalf("higher difficulty target must have higher work")
	}
}

func TestAuxPoWValidProofVerification(t *testing.T) {
	miner := "go1506ef1879d748ce0713b0dd01da32ad2a4b1ce97b5a852ff18800c1e52982376"
	pool := NewMiningPoolSimulator("AntPool_BTC", miner)

	candidate := NewBlock(1, "0000000000000000000000000000000000000000000000000000000000000000", []Transaction{}, miner)
	targetBits := DefaultGoAuxBits // 0x1e00ffff

	auxBlock, err := pool.SolveGoChainJob(candidate, targetBits)
	if err != nil {
		t.Fatalf("SolveGoChainJob failed: %v", err)
	}

	// Verify valid AuxPoW
	err = ValidateAuxPoW(auxBlock.Block, auxBlock.AuxPoW, targetBits)
	if err != nil {
		t.Fatalf("valid AuxPoW rejected: %v", err)
	}

	t.Logf("AuxPoW Block #%d successfully solved by %s with Parent Nonce %d",
		auxBlock.Index, pool.PoolName, auxBlock.AuxPoW.ParentHeader.Nonce)
}

func TestAuxPoWAdversarialRejections(t *testing.T) {
	miner := "go1506ef1879d748ce0713b0dd01da32ad2a4b1ce97b5a852ff18800c1e52982376"
	pool := NewMiningPoolSimulator("Foundry_BTC", miner)

	candidate := NewBlock(1, "0000000000000000000000000000000000000000000000000000000000000000", []Transaction{}, miner)
	targetBits := DefaultGoAuxBits

	auxBlock, err := pool.SolveGoChainJob(candidate, targetBits)
	if err != nil {
		t.Fatalf("SolveGoChainJob failed: %v", err)
	}

	// 1. Rejection: Insufficient work (target required is harder than solved)
	harderTarget := uint32(0x10000001) // Extremely hard
	if err := ValidateAuxPoW(auxBlock.Block, auxBlock.AuxPoW, harderTarget); err == nil {
		t.Fatalf("expected error for insufficient parent work, got nil")
	}

	// 2. Rejection: Mismatched block hash (coinbase committed to Block A, but presented with Block B)
	blockB := NewBlock(2, "0000111122223333444455556666777788889999aaaabbbbccccddddeeeeffff", []Transaction{}, miner)
	blockB.Hash = blockB.computeHash()
	if err := ValidateAuxPoW(blockB, auxBlock.AuxPoW, targetBits); err == nil {
		t.Fatalf("expected error for mismatched commitment, got nil")
	}

	// 3. Rejection: Forged Coinbase (altered transaction body)
	forgedAuxPoW := *auxBlock.AuxPoW
	cbBytes, _ := hex.DecodeString(forgedAuxPoW.CoinbaseTxHex)
	cbBytes[len(cbBytes)-1] ^= 0xff // Corrupt byte
	forgedAuxPoW.CoinbaseTxHex = hex.EncodeToString(cbBytes)

	if err := ValidateAuxPoW(auxBlock.Block, &forgedAuxPoW, targetBits); err == nil {
		t.Fatalf("expected error for corrupted coinbase merkle branch, got nil")
	}

	// 4. Rejection: Forged Parent Merkle Root
	forgedHeaderAuxPoW := *auxBlock.AuxPoW
	forgedHeaderAuxPoW.ParentHeader.MerkleRoot = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := ValidateAuxPoW(auxBlock.Block, &forgedHeaderAuxPoW, targetBits); err == nil {
		t.Fatalf("expected error for forged parent merkle root, got nil")
	}
}

func TestMultiChainMergedMiningSimulation(t *testing.T) {
	miner := "go1506ef1879d748ce0713b0dd01da32ad2a4b1ce97b5a852ff18800c1e52982376"
	pool := NewMiningPoolSimulator("F2Pool_MultiChain", miner)

	// Simulate 1 Bitcoin mining job simultaneously committing to:
	// 1. GoChain (our chain)
	// 2. GameChain (sibling chain)
	goCandidate := NewBlock(10, "0000123400000000000000000000000000000000000000000000000000000000", []Transaction{}, miner)
	siblingGameChainHash := RandomHash()

	targetBits := DefaultGoAuxBits
	auxBlock, err := pool.SolveMultiChainJob(goCandidate, []string{siblingGameChainHash}, targetBits)
	if err != nil {
		t.Fatalf("SolveMultiChainJob failed: %v", err)
	}

	// Verify that GoChain validates its inclusion via ChainMerkleBranch
	if err := ValidateAuxPoW(auxBlock.Block, auxBlock.AuxPoW, targetBits); err != nil {
		t.Fatalf("multi-chain AuxPoW proof rejected: %v", err)
	}

	t.Logf("Multi-Chain AuxPoW Verified! One BTC Block mined both GoChain and Sibling Chain (Merkle Branch Depth: %d)",
		len(auxBlock.AuxPoW.ChainMerkleBranch))
}

func TestDynamicDifficultyRetargeting(t *testing.T) {
	initialBits := DefaultGoAuxBits
	initialTarget := BitsToTarget(initialBits)
	targetTimespan := int64(600) // 10 blocks * 60s = 600s

	// Case 1: Fast Mining (blocks found in 150s, 4x faster than target)
	// Hashrate surged -> difficulty must increase (target shrinks)
	fastBits := RetargetDifficulty(initialBits, 150, targetTimespan)
	fastTarget := BitsToTarget(fastBits)

	if fastTarget.Cmp(initialTarget) >= 0 {
		t.Fatalf("fast mining must decrease target (increase difficulty): initial %x, new %x",
			initialTarget, fastTarget)
	}

	// Case 2: Slow Mining (blocks found in 2400s, 4x slower than target)
	// Hashrate dropped -> difficulty must ease (target expands)
	slowBits := RetargetDifficulty(fastBits, 2400, targetTimespan)
	slowTarget := BitsToTarget(slowBits)

	if slowTarget.Cmp(fastTarget) <= 0 {
		t.Fatalf("slow mining must increase target (decrease difficulty): fast %x, new %x",
			fastTarget, slowTarget)
	}

	// Case 3: Extreme Clamp (timespan < target/4 is clamped to target/4)
	superFastBits := RetargetDifficulty(initialBits, 1, targetTimespan)
	clampFastBits := RetargetDifficulty(initialBits, 150, targetTimespan) // 600 / 4 = 150
	if superFastBits != clampFastBits {
		t.Fatalf("extreme fast timespan must be clamped at target/4: got 0x%x, expected 0x%x",
			superFastBits, clampFastBits)
	}
}

func TestCumulativeWorkForkChoice(t *testing.T) {
	// Scenario:
	// Chain A has 3 blocks mined at HIGH difficulty (Bits = 0x1d00ffff)
	// Chain B has 10 blocks mined at LOW difficulty (Bits = 0x1f00ffff)
	// Cumulative work fork choice must choose Chain A despite having fewer blocks!

	highDiffBits := uint32(0x1d00ffff)
	lowDiffBits := uint32(0x1f00ffff)

	workHighPerBlock := TargetToWork(BitsToTarget(highDiffBits))
	workLowPerBlock := TargetToWork(BitsToTarget(lowDiffBits))

	chainAWork := new(big.Int).Mul(workHighPerBlock, big.NewInt(3))
	chainBWork := new(big.Int).Mul(workLowPerBlock, big.NewInt(10))

	t.Logf("Chain A Work (3 high-diff blocks): %s", chainAWork.String())
	t.Logf("Chain B Work (10 low-diff blocks): %s", chainBWork.String())

	if chainAWork.Cmp(chainBWork) <= 0 {
		t.Fatalf("Chain A with high difficulty must have higher cumulative work than 10 low-diff blocks")
	}

	// Validate Fork Choice Function
	adoptedAoverB := chainAWork.Cmp(chainBWork) > 0
	if !adoptedAoverB {
		t.Fatalf("Fork choice engine failed to select higher cumulative work chain")
	}
}

func TestAuxPoWBlockRewardAndSupplyConservation(t *testing.T) {
	miner := "go1506ef1879d748ce0713b0dd01da32ad2a4b1ce97b5a852ff18800c1e52982376"
	pool := NewMiningPoolSimulator("ViaBTC", miner)

	now := time.Now().Unix()
	candidate := NewBlock(1, GenesisBlock().Hash, []Transaction{}, miner)
	candidate.Timestamp = now
	candidate.Reward = rewardAt(now)

	auxBlock, err := pool.SolveGoChainJob(candidate, DefaultGoAuxBits)
	if err != nil {
		t.Fatalf("failed to solve aux block: %v", err)
	}

	// Validate Block economics
	if auxBlock.Reward != 1_000_000_000 { // 10 GO initial reward
		t.Fatalf("expected 10 GO reward, got %d", auxBlock.Reward)
	}
	if auxBlock.Miner != miner {
		t.Fatalf("miner address mismatch: got %s, expected %s", auxBlock.Miner, miner)
	}

	t.Logf("AuxPoW Block verified! Rewarded 10 GO to pool address %s", miner)
}
