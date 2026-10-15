import assert from "node:assert/strict";
import BN from "bn.js";
import { readFile, writeFile } from "node:fs/promises";
import { Connection, Keypair, PublicKey } from "@solana/web3.js";
import {
  getMint,
  getOrCreateAssociatedTokenAccount,
  mintTo,
  transfer,
} from "@solana/spl-token";
import { Market, formatUnits, outputFor, parseUnits } from "../src/market";
import { Deployment, keyWallet, PROGRAM } from "../src/solana";
const d: Deployment = JSON.parse(
  await readFile("public/deployment.json", "utf8"),
);
if (d.network !== "localnet")
  throw Error(
    "Integration test runs only on localnet; never spends devnet or mainnet funds.",
  );
const payer = Keypair.fromSecretKey(
  Uint8Array.from(JSON.parse(await readFile(".local/deployer.json", "utf8"))),
);
const user = Keypair.generate();
const c = new Connection(d.rpc, "confirmed");
const m = new Market(d, keyWallet(user));
await m.check();
const air = await c.requestAirdrop(user.publicKey, 1000000000);
await c.confirmTransaction(air, "confirmed");
for (const mintStr of [d.mintA, d.mintB]) {
  const mint = new PublicKey(mintStr);
  const ata = await getOrCreateAssociatedTokenAccount(
    c,
    payer,
    mint,
    user.publicKey,
  );
  await mintTo(
    c,
    payer,
    mint,
    ata.address,
    payer,
    mint.equals(m.mint("GO")) ? 100000000n : 10000000000n,
  );
}
const records: Record<string, unknown>[] = [];
async function rejected(fn: () => Promise<unknown>, label: string) {
  const before = await m.reserves(),
    balanceBefore = await m.snapshot();
  let failed = false;
  try {
    await fn();
  } catch {
    failed = true;
  }
  assert(failed, label + " must reject");
  assert.deepEqual(await m.reserves(), before);
  const after = await m.snapshot();
  assert.equal(after.go, balanceBefore.go);
  assert.equal(after.usd, balanceBefore.usd);
  records.push({
    test: label,
    result: "rejected without changing token balances or reserves",
  });
}
let q = await m.quoteSwap("testUSD", "100");
await rejected(
  () => m.swap({ ...q, minimum: q.output + 1n }),
  "Impossible minimum output",
);
await rejected(
  () =>
    m.program.methods
      .swap(
        d.symbolA === q.symbol,
        new BN(q.input.toString()),
        new BN(1),
        new BN(0),
      )
      .accountsStrict(m.accounts())
      .rpc(),
  "On-chain expired quote",
);
for (const [symbol, amount] of [
  ["testUSD", "100"],
  ["GO", "1"],
]) {
  q = await m.quoteSwap(symbol, amount);
  const before = await m.reserves(),
    b = await m.snapshot();
  const sig = await m.swap(q);
  const after = await m.reserves(),
    a = await m.snapshot();
  const aToB = d.symbolA === symbol;
  assert.equal(aToB ? after.a - before.a : after.b - before.b, q.input);
  assert.equal(aToB ? before.b - after.b : before.a - after.a, q.output);
  assert(after.a * after.b >= before.a * before.b);
  const received =
    symbol === "GO"
      ? parseUnits(a.usd) - parseUnits(b.usd)
      : parseUnits(a.go) - parseUnits(b.go);
  assert.equal(received, q.output);
  records.push({
    test: "Swap " + symbol,
    signature: sig,
    input: formatUnits(q.input),
    output: formatUnits(q.output),
  });
}
const deposit = await m.quoteDeposit("2");
const beforeLP = await m.balance(new PublicKey(d.lpMint));
const depositSig = await m.deposit(deposit);
const afterLP = await m.balance(new PublicKey(d.lpMint));
assert.equal(afterLP - beforeLP, deposit.shares);
records.push({
  test: "Liquidity deposit",
  signature: depositSig,
  shares: formatUnits(deposit.shares),
});
const withdrawal = await m.quoteWithdraw(formatUnits(deposit.shares));
const withdrawSig = await m.withdraw(withdrawal);
assert.equal(await m.balance(new PublicKey(d.lpMint)), beforeLP);
records.push({ test: "Liquidity withdrawal", signature: withdrawSig });
await rejected(() => m.withdraw(withdrawal), "Withdraw without LP ownership");
const lockedAttackSlot = await c.getSlot();
await rejected(
  () =>
    m.program.methods
      .withdraw(new BN(1), new BN(1), new BN(1), new BN(lockedAttackSlot + 150))
      .accountsStrict({
        ...m.liquidityAccounts(),
        userLp: new PublicKey(d.lockedLP),
      })
      .rpc(),
  "Attempt to redeem permanently locked LP",
);
const wrong = { ...m.accounts(), vaultA: new PublicKey(d.vaultB) };
const attack = await m.quoteSwap("testUSD", "1");
await rejected(
  () =>
    m.program.methods
      .swap(
        d.symbolA === "testUSD",
        new BN(attack.input.toString()),
        new BN(1),
        new BN(attack.slot + 150),
      )
      .accountsStrict(wrong)
      .rpc(),
  "Substitute wrong vault",
);
// A direct donation changes reserves but does not mint LP shares.
const supplyBefore = (await getMint(c, new PublicKey(d.lpMint))).supply;
const source = (
  await getOrCreateAssociatedTokenAccount(
    c,
    payer,
    m.mint("GO"),
    user.publicKey,
  )
).address;
const vault = new PublicKey(d.symbolA === "GO" ? d.vaultA : d.vaultB);
await transfer(c, user, source, vault, user, 1000000n);
assert.equal((await getMint(c, new PublicKey(d.lpMint))).supply, supplyBefore);
records.push({
  test: "Direct vault donation",
  result: "Reserves increase; no LP shares minted",
});
const report = {
  network: d.network,
  program: PROGRAM.toBase58(),
  pool: d.pool,
  records,
};
await writeFile(".local/verification.json", JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
