import { mkdir, readFile, writeFile } from "node:fs/promises";
import {
  Connection,
  Keypair,
  PublicKey,
  SystemProgram,
  SYSVAR_RENT_PUBKEY,
} from "@solana/web3.js";
import {
  createMint,
  getOrCreateAssociatedTokenAccount,
  mintTo,
  TOKEN_PROGRAM_ID,
} from "@solana/spl-token";
import BN from "bn.js";
import {
  PROGRAM,
  keyWallet,
  context,
  assertNetwork,
  Deployment,
  poolAddresses,
} from "../src/solana";
const network = process.argv.includes("--localnet") ? "localnet" : "devnet";
const rpc =
  network === "devnet"
    ? "https://api.devnet.solana.com"
    : process.env.GO_POOLS_LOCAL_RPC || "http://127.0.0.1:8897";
const c = new Connection(rpc, {
  commitment: "confirmed",
  disableRetryOnRateLimit: true,
});
await assertNetwork(c, { network, rpc });
await mkdir(".local", { recursive: true });
await mkdir("public", { recursive: true });
const payer = Keypair.fromSecretKey(
  Uint8Array.from(JSON.parse(await readFile(".local/deployer.json", "utf8"))),
);
console.log("Dedicated test wallet:", payer.publicKey.toBase58());
if ((await c.getBalance(payer.publicKey)) < 200000000) {
  const s = await c.requestAirdrop(
    payer.publicKey,
    network === "localnet" ? 10000000000 : 1000000000,
  );
  await c.confirmTransaction(s, "confirmed");
}
if (!(await c.getAccountInfo(PROGRAM))?.executable)
  throw Error("Deploy our goamm program before initializing the pool.");
const checkpoint = `.local/${network}-goamm-setup.json`;
let saved: Record<string, string> = {};
try {
  saved = JSON.parse(await readFile(checkpoint, "utf8"));
} catch (e: any) {
  if (e.code !== "ENOENT") throw e;
}
async function persist() {
  await writeFile(checkpoint, JSON.stringify(saved, null, 2), { mode: 0o600 });
}
for (const symbol of ["GO", "testUSD"]) {
  if (!saved[symbol]) {
    saved[symbol] = (
      await createMint(c, payer, payer.publicKey, null, 6)
    ).toBase58();
    await persist();
    console.log("Test token", symbol, saved[symbol]);
  }
}
const sorted = [
  { mint: new PublicKey(saved.GO), symbol: "GO" },
  { mint: new PublicKey(saved.testUSD), symbol: "testUSD" },
].sort((a, b) => Buffer.compare(a.mint.toBuffer(), b.mint.toBuffer()));
const [a, b] = sorted;
const addresses = poolAddresses(a.mint, b.mint);
const { program } = context(c, keyWallet(payer));
for (const token of sorted) {
  const ata = await getOrCreateAssociatedTokenAccount(
    c,
    payer,
    token.mint,
    payer.publicKey,
  );
  if (ata.amount < 1000000000000n)
    await mintTo(
      c,
      payer,
      token.mint,
      ata.address,
      payer,
      1000000000000n - ata.amount,
    );
}
if (!(await c.getAccountInfo(addresses.pool))) {
  console.log(
    "Initialize custom AMM:",
    await program.methods
      .initialize()
      .accountsStrict({
        payer: payer.publicKey,
        mintA: a.mint,
        mintB: b.mint,
        ...addresses,
        tokenProgram: TOKEN_PROGRAM_ID,
        systemProgram: SystemProgram.programId,
        rent: SYSVAR_RENT_PUBKEY,
      })
      .rpc(),
  );
}
const lp = await getOrCreateAssociatedTokenAccount(
  c,
  payer,
  addresses.lpMint,
  payer.publicKey,
);
const ataA = await getOrCreateAssociatedTokenAccount(
    c,
    payer,
    a.mint,
    payer.publicKey,
  ),
  ataB = await getOrCreateAssociatedTokenAccount(
    c,
    payer,
    b.mint,
    payer.publicKey,
  );
if ((await c.getTokenSupply(addresses.lpMint)).value.amount === "0") {
  const maxA = new BN(a.symbol === "GO" ? "1000000000" : "100000000000"),
    maxB = new BN(b.symbol === "GO" ? "1000000000" : "100000000000");
  console.log(
    "Seed custom pool:",
    await program.methods
      .deposit(maxA, maxB, new BN(1), new BN((await c.getSlot()) + 150))
      .accountsStrict({
        user: payer.publicKey,
        ...addresses,
        userA: ataA.address,
        userB: ataB.address,
        userLp: lp.address,
        tokenProgram: TOKEN_PROGRAM_ID,
      })
      .rpc(),
  );
}
const d: Deployment = {
  network,
  rpc,
  program: PROGRAM.toBase58(),
  pool: addresses.pool.toBase58(),
  mintA: a.mint.toBase58(),
  mintB: b.mint.toBase58(),
  symbolA: a.symbol,
  symbolB: b.symbol,
  decimals: 6,
  vaultA: addresses.vaultA.toBase58(),
  vaultB: addresses.vaultB.toBase58(),
  lpMint: addresses.lpMint.toBase58(),
  lockedLP: addresses.lockedLp.toBase58(),
};
await writeFile("public/deployment.json", JSON.stringify(d, null, 2));
console.log("Custom Go AMM ready:", JSON.stringify(d));
