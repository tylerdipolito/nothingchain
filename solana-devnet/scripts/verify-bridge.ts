import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { getMint } from "@solana/spl-token";
import { Bridge, runBridge } from "./bridge";
import { exclusive, Journal } from "./bridge-core";

if (!process.argv.includes("--isolated"))
  throw Error(
    "Requires --isolated: creates fresh test wallets/mints and mines blocks on a dedicated checkpoint node",
  );
const directory = resolve(`.local/bridge-verification-${Date.now()}`);
await mkdir(directory, { recursive: true });
const node = process.env.GO_BRIDGE_TEST_NODE || "http://127.0.0.1:8086";
const rpc = process.env.GO_BRIDGE_TEST_RPC || "http://127.0.0.1:8897";
const steps: string[] = [];
const run = (command: string, ...args: string[]) =>
  runBridge(directory, command, args) as Promise<any>;
await run("init", node, rpc);
await run("mine", "2");
let s = await run("wrap", "10.00000001");
assert.equal(s.backing.supply_atoms, "0");
await assert.rejects(run("wrap", "1"), /pending/);
steps.push(
  "No mint before deposit confirmations; second operation rejected while pending",
);
s = await run("mine", "3");
assert.equal(s.backing.supply_atoms, "1000000001");
assert.equal(s.backing.custody_atoms, "1000000001");
steps.push(
  "Eight-decimal lock/mint exact; initial Go signatures accepted by real node",
);
// Simulate loss of the completion write after a successful mint. Signed intent remains
// exactly what a crash before acknowledgement would leave; never re-sign/re-mint.
await exclusive(directory, async () => {
  const j = new Journal(directory);
  await j.load();
  const op = j.state.operations.find((o) => o.kind === "wrap")!;
  op.done = false;
  op.solConfirmed = false;
  await j.save();
});
s = await run("recover");
assert.equal(s.backing.supply_atoms, "1000000001");
const mintSignature = s.operations.find(
  (o: any) => o.kind === "wrap",
).solSignature;
s = await run("recover");
assert.equal(
  s.operations.find((o: any) => o.kind === "wrap").solSignature,
  mintSignature,
);
steps.push(
  "Reopened pending mint intent recovers original finalized signature, no duplicate issuance",
);
await run("seed-pool", "5", "500");
const before = await run("status");
const quote = await run("quote", "testUSD", "10");
await run("swap", "testUSD", "10", quote.minimum);
s = await run("status");
assert.equal(s.pool.testUSD, "510.00000000");
assert.equal(s.pool.wGO, "4.90224916");
assert.equal(quote.output, "0.09775084");
assert.equal(s.user.testUSD, "9490.00000000");
assert.equal(s.backing.supply_atoms, before.backing.supply_atoms);
steps.push(
  "Actual swap changes pool/user balances by exact quote without changing wrapped supply",
);
s = await run("unwrap", "1.00000001");
assert.equal(s.backing.supply_atoms, "900000000");
assert.equal(s.backing.pending_withdrawal_atoms, "100000001");
assert.equal(s.backing.custody_atoms, "1000000001");
const releaseSignature = s.operations.find(
  (o: any) => o.kind === "unwrap",
).goSignature;
await run("recover");
await run("mine", "1"); // Intermediate release inclusion must not double-count liabilities.
s = await run("mine", "2");
assert.equal(s.backing.custody_atoms, "900000000");
assert.equal(s.backing.supply_atoms, "900000000");
assert.equal(s.backing.pending_withdrawal_atoms, "0");
assert.equal(s.user.GO, "71"); // Mining rewards plus the exact returned 1.00000001 GO.
assert.equal(
  s.operations.find((o: any) => o.kind === "unwrap").goSignature,
  releaseSignature,
);
await run("recover");
steps.push(
  "Finalized burn queues exactly one GO release; restart/recover and 1+2 confirmation path preserve backing",
);
// Deliberate fake fork/history fixtures are confined to this disposable journal.
await exclusive(directory, async () => {
  const j = new Journal(directory);
  await j.load();
  const original = structuredClone(j.state);
  j.state.operations.find((o) => o.kind === "wrap")!.anchor!.hash =
    "removed-by-fork";
  await j.save();
  await assert.rejects(runWithoutLock(), /reorganization/);
  j.state = original;
  await j.save();
  async function runWithoutLock() {
    const b = new Bridge(directory);
    await b.load();
  }
});
steps.push("Changed recorded source anchor halts bridge before another action");
const final = await run("status");
const b = new Bridge(directory);
await b.load();
assert.equal(
  (await getMint(b.c, b.mint(), "finalized")).supply.toString(),
  final.backing.supply_atoms,
);
await writeFile(
  `${directory}/verification.json`,
  JSON.stringify({ steps, final }, null, 2),
);
console.log(JSON.stringify({ directory, steps, final }, null, 2));
