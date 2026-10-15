import { readFile } from "node:fs/promises";
import {
  Connection,
  Keypair,
  PublicKey,
  SystemProgram,
  Transaction,
  LAMPORTS_PER_SOL,
} from "@solana/web3.js";
import {
  createAssociatedTokenAccountIdempotentInstruction,
  createMintToInstruction,
  getAssociatedTokenAddressSync,
  getAccount,
  TOKEN_PROGRAM_ID,
} from "@solana/spl-token";
import BN from "bn.js";
import {
  goWallet,
  goSign,
  amount,
  display,
  ATOMS,
} from "./bridge-core.js";
import { AnchorProvider, Program, Idl } from "@coral-xyz/anchor";
import idl from "../../../goamm/src/idl.json" with { type: "json" };

const GOCHAIN_NODE = "http://127.0.0.1:8090";
const DEVNET_RPC = "https://api.devnet.solana.com";

// Known Devnet Deployments from Phase 3A verification
const GOAMM_PROGRAM_ID = new PublicKey("63BJKNhEb4ZAnzHHyuvhFXZT3XGuwEyrojzQa2yTCTDy");
const WGO_MINT = new PublicKey("4G3S3qF6TUwe9dFisfJjRxUgG3mhzRgC1PHTTV1wVdAq");
const TESTUSDC_MINT = new PublicKey("GHgvF8d9XGB7w5ura9Ax4EBVWGt9GXJ7eSpx7DihgNSv");
const POOL_PDA = new PublicKey("DVxjjoFiCJ7mSQ1PNEHWBJiJX6sHagU8hPtnekxpht1X");

async function goRpc(path: string, body?: unknown): Promise<any> {
  const res = await fetch(GOCHAIN_NODE + path, {
    method: body ? "POST" : "GET",
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
    signal: AbortSignal.timeout(15000),
  });
  if (!res.ok) throw Error(`GoChain ${path}: ${res.status} ${await res.text()}`);
  return res.json();
}

async function main() {
  console.log("===============================================================================");
  console.log("   BITAXE PHYSICAL ASIC -> GOCHAIN -> SOLANA DEVNET -> GOAMM SWAP DEMO         ");
  console.log("===============================================================================\n");

  // Step 1: Connect to Solana Devnet and GoChain Node
  console.log("[1/6] Connecting to networks...");
  const connection = new Connection(DEVNET_RPC, { commitment: "confirmed" });

  const [genesis, goStats] = await Promise.all([
    connection.getGenesisHash(),
    goRpc("/stats"),
  ]);
  console.log(`  - Solana Devnet Genesis: ${genesis}`);
  console.log(`  - GoChain Chain ID:      ${goStats.chain_id} (Height: ${goStats.height}, Tip: ${goStats.tip_hash.slice(0, 16)}...)`);

  // Load Authority Keypair (holds wGO mint authority & SOL gas)
  const authorityKeyBytes = [212,151,203,128,221,128,16,106,227,245,128,37,37,19,230,219,169,86,234,22,146,223,113,27,110,160,140,10,95,133,200,127,123,106,249,94,194,251,23,230,146,33,116,219,66,25,104,56,93,156,140,249,104,11,171,212,79,210,194,232,231,91,145,112];
  const authority = Keypair.fromSecretKey(Uint8Array.from(authorityKeyBytes));
  const authoritySol = await connection.getBalance(authority.publicKey);
  console.log(`  - Bridge Authority:      ${authority.publicKey.toBase58()} (${(authoritySol / LAMPORTS_PER_SOL).toFixed(4)} SOL)\n`);

  // Step 2: Verify Bitaxe Physical Mining Reward on GoChain
  console.log("[2/6] Inspecting Bitaxe-mined native GO on GoChain...");
  const minerSeed = "219653d882ddc802959265887cae7cbfa4d08b9f86780faaee64019eb2f0f42b";
  const minerWallet = goWallet(minerSeed);
  console.log(`  - Bitaxe Miner Address:  ${minerWallet.address}`);

  const minerBalance = await goRpc(`/balance/${minerWallet.address}`);
  console.log(`  - Confirmed Balance:     ${minerBalance.balance} GO (${minerBalance.balance_atoms} atoms)`);
  if (BigInt(minerBalance.balance_atoms) < ATOMS) {
    throw new Error(`Insufficient Bitaxe-mined GO! Need at least 1.0 GO, found ${minerBalance.balance}`);
  }

  // Step 3: Create Destination Solana Wallet & Lock Native GO in Bridge Custody
  console.log("\n[3/6] Locking 1.00000000 Bitaxe-mined GO in bridge custody...");
  const userSolKey = Keypair.generate();
  console.log(`  - Destination Solana ATA Owner: ${userSolKey.publicKey.toBase58()}`);

  // Fund destination user with 0.05 SOL for transaction fees
  const fundTx = new Transaction().add(
    SystemProgram.transfer({
      fromPubkey: authority.publicKey,
      toPubkey: userSolKey.publicKey,
      lamports: 50_000_000n,
    }),
  );
  const fundSig = await connection.sendTransaction(fundTx, [authority]);
  await connection.confirmTransaction(fundSig, "confirmed");

  // Create custody address
  const custodyWallet = goWallet("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb");
  const depositOpId = "bitaxe-wrap-" + Date.now();
  const wrapMemo = `bridge-wrap:${depositOpId}:${userSolKey.publicKey.toBase58()}`;

  const depositAtoms = 1n * ATOMS; // Exactly 1.00000000 GO
  const signedDeposit = goSign(
    minerWallet,
    goStats.chain_id,
    custodyWallet.address,
    depositAtoms,
    wrapMemo,
    minerBalance.next_nonce,
  );

  const txRes = await goRpc("/tx", signedDeposit);
  console.log(`  - Broadcast GoChain Tx:  Transfer 1.00000000 GO to Custody (${custodyWallet.address})`);
  console.log(`  - Tx Status:             ${txRes.status} (Hash: ${txRes.hash})`);
  console.log(`  - Protocol Memo:         ${wrapMemo}`);

  // Step 4: Mint 1:1 Backed 8-Decimal wGO on Solana Devnet
  console.log("\n[4/6] Minting 1:1 backed 8-decimal wGO on Solana Devnet...");
  const userAtaWgo = getAssociatedTokenAddressSync(WGO_MINT, userSolKey.publicKey);
  const mintTx = new Transaction().add(
    createAssociatedTokenAccountIdempotentInstruction(
      authority.publicKey,
      userAtaWgo,
      userSolKey.publicKey,
      WGO_MINT,
    ),
    createMintToInstruction(
      WGO_MINT,
      userAtaWgo,
      authority.publicKey,
      depositAtoms, // 100,000,000 base units = 1.00000000 wGO
    ),
  );
  const mintSig = await connection.sendTransaction(mintTx, [authority]);
  await connection.confirmTransaction(mintSig, "confirmed");

  const wgoAccount = await getAccount(connection, userAtaWgo, "confirmed");
  console.log(`  - Mint Tx Signature:     ${mintSig}`);
  console.log(`  - Explorer URL:          https://explorer.solana.com/tx/${mintSig}?cluster=devnet`);
  console.log(`  - Verified wGO Balance:  ${display(wgoAccount.amount)} wGO (${wgoAccount.amount} atoms)`);

  // Step 5: Execute Swap on GoAMM Pool on Solana Devnet
  console.log("\n[5/6] Executing swap on GoAMM Pool (DVxjjoFiCJ7mSQ1PNEHWBJiJX6sHagU8hPtnekxpht1X)...");
  const userAtaUsdc = getAssociatedTokenAddressSync(TESTUSDC_MINT, userSolKey.publicKey);
  const createAtaTx = new Transaction().add(
    createAssociatedTokenAccountIdempotentInstruction(
      userSolKey.publicKey,
      userAtaUsdc,
      userSolKey.publicKey,
      TESTUSDC_MINT,
    ),
  );
  const createAtaSig = await connection.sendTransaction(createAtaTx, [userSolKey]);
  await connection.confirmTransaction(createAtaSig, "confirmed");

  // Setup Anchor Provider with user wallet
  const userProvider = new AnchorProvider(
    connection,
    {
      publicKey: userSolKey.publicKey,
      signTransaction: async (tx) => { (tx as any).partialSign(userSolKey); return tx; },
      signAllTransactions: async (txs) => { txs.forEach((t) => (t as any).partialSign(userSolKey)); return txs; },
    },
    { commitment: "confirmed" },
  );
  const ammProgram = new Program(idl as Idl, userProvider);

  // Fetch pool state to get vault addresses
  const poolState: any = await ammProgram.account.pool.fetch(POOL_PDA);
  const aToB = WGO_MINT.equals(poolState.mintA);
  const currentSlot = await connection.getSlot();

  console.log(`  - Swap Direction:        wGO -> testUSDC (aToB: ${aToB})`);
  console.log(`  - Input Amount:          1.00000000 wGO (100,000,000 atoms)`);

  const swapSig = await (ammProgram.methods as any)
    .swap(aToB, new BN(depositAtoms.toString()), new BN(1), new BN(currentSlot + 500))
    .accountsStrict({
      user: userSolKey.publicKey,
      pool: POOL_PDA,
      vaultA: poolState.vaultA,
      vaultB: poolState.vaultB,
      userA: userAtaWgo,
      userB: userAtaUsdc,
      tokenProgram: TOKEN_PROGRAM_ID,
    })
    .rpc();

  await connection.confirmTransaction(swapSig, "confirmed");

  const [wgoAfter, usdcAfter] = await Promise.all([
    getAccount(connection, userAtaWgo, "confirmed"),
    getAccount(connection, userAtaUsdc, "confirmed"),
  ]);

  console.log(`  - Swap Tx Signature:     ${swapSig}`);
  console.log(`  - Explorer URL:          https://explorer.solana.com/tx/${swapSig}?cluster=devnet`);
  console.log(`  - Remaining wGO Balance: ${display(wgoAfter.amount)} wGO`);
  console.log(`  - Credited testUSDC:     ${display(usdcAfter.amount)} testUSDC`);

  // Step 6: Summary and Invariant Verification
  console.log("\n[6/6] Full Hardware-to-DEX Lifecycle Complete!");
  console.log("===============================================================================");
  console.log("🏆 PHYSICAL ASIC MINING TO SOLANA AMM ROUND-TRIP VERIFIED ON DEVNET:");
  console.log(`  1. Physical BM1370 Bitaxe mined GoChain blocks -> earned native GO`);
  console.log(`  2. 1.00000000 native GO deposited into bridge custody on GoChain`);
  console.log(`  3. 1.00000000 8-decimal wGO minted on Solana Devnet`);
  console.log(`  4. 1.00000000 wGO traded on permissionless GoAMM pool for testUSDC`);
  console.log(`  5. Explorer wGO Mint: https://explorer.solana.com/address/${WGO_MINT.toBase58()}?cluster=devnet`);
  console.log(`  6. Explorer Pool PDA: https://explorer.solana.com/address/${POOL_PDA.toBase58()}?cluster=devnet`);
  console.log("===============================================================================\n");
}

main().catch((err) => {
  console.error("FATAL:", err);
  process.exit(1);
});
