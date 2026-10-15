import assert from "node:assert/strict";
import { resolve } from "node:path";
import { writeFile } from "node:fs/promises";
import { getMint } from "@solana/spl-token";
import { Bridge, runBridge } from "./bridge";
if (process.argv[2] !== "--isolated")
  throw Error("Only run from the opt-in Go reorganization integration test");
const directory = resolve(`.local/bridge-reorg-${Date.now()}`),
  node = process.argv[3];
const run = (c: string, a: string[] = []) =>
  runBridge(directory, c, a) as Promise<any>;
await run("init", [
  node,
  process.env.GO_BRIDGE_TEST_RPC || "http://127.0.0.1:8897",
]);
await run("mine", ["1"]);
await run("wrap", ["10"]);
const before = await run("mine", ["3"]);
assert.equal(before.backing.supply_atoms, "1000000000");
const b = new Bridge(directory);
await b.load();
const response = await fetch(node + "/test/reorg", { method: "POST" });
assert.equal(response.status, 200);
assert.equal((await response.json()).adopted, true);
await assert.rejects(run("status"), /reorganization/);
await assert.rejects(run("wrap", ["1"]), /reorganization/);
await assert.rejects(run("unwrap", ["1"]), /reorganization/);
assert.equal((await getMint(b.c, b.mint(), "finalized")).supply, 1000000000n);
const custody = await b.go("/balance/" + b.config.custody);
assert.equal(custody.balance_atoms, 0);
const report = {
  result:
    "Real longer valid Go fork adopted; removed bridged deposit. Bridge halted status/wrap/unwrap without issuing or releasing more.",
  supply_atoms: "1000000000",
  custody_atoms: "0",
  warning:
    "Halting cannot undo circulated wGO; operator must resolve the deficit.",
};
await writeFile(
  `${directory}/reorg-verification.json`,
  JSON.stringify(report, null, 2),
);
console.log(JSON.stringify({ directory, ...report }, null, 2));
