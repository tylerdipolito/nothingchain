import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { access, mkdir, readFile, unlink, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { Bridge, runBridge } from "./bridge";
if (!process.argv.includes("--isolated"))
  throw Error(
    "Requires --isolated; only dedicated checkpoint node/local validator",
  );
const directory = resolve(`.local/bridge-crash-${Date.now()}`);
await mkdir(directory, { recursive: true });
const run = (command: string, args: string[] = [], id?: string) =>
  runBridge(directory, command, args, id) as Promise<any>;
const report: string[] = [];
async function crash(
  phase: string,
  command: string,
  args: string[] = [],
  id?: string,
) {
  let output = "";
  const child = spawn(
    process.execPath,
    [
      "--import",
      "tsx",
      resolve("scripts/bridge-crash-worker.ts"),
      "--isolated",
      directory,
      phase,
      command,
      id || "-",
      ...args,
    ],
    { windowsHide: true, stdio: ["ignore", "pipe", "pipe"] },
  );
  child.stdout.on("data", (b) => (output += b));
  child.stderr.on("data", (b) => (output += b));
  const exited = new Promise<void>((r) => child.once("exit", () => r()));
  try {
    let found = false;
    for (let i = 0; i < 450; i++) {
      if (child.exitCode !== null) throw Error(output);
      try {
        await access(`${directory}/paused.marker`);
        found = true;
        break;
      } catch {}
      await new Promise((r) => setTimeout(r, 100));
    }
    assert(found, `No ${phase} marker: ${output}`);
    child.kill("SIGKILL");
    await exited;
    const owner = await readFile(`${directory}/operator.lock`, "utf8");
    assert.equal(Number(owner), child.pid);
    // Only our own terminated child lock can be removed; production never removes stale locks.
    await unlink(`${directory}/operator.lock`);
    await unlink(`${directory}/paused.marker`);
    report.push(
      `Killed owned worker at ${phase}; recovered its durable intent`,
    );
  } finally {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill("SIGKILL");
      await exited;
    }
  }
}
await run("init", [
  process.env.GO_BRIDGE_TEST_NODE || "http://127.0.0.1:8086",
  process.env.GO_BRIDGE_TEST_RPC || "http://127.0.0.1:8897",
]);
await run("mine", ["2"]);
const wrapId = "11111111-1111-4111-8111-111111111111";
await crash("deposit-before-send", "wrap", ["10"], wrapId);
await run("recover");
const b = new Bridge(directory);
await b.load();
for (let i = 0; i < 3; i++) await b.go("/mine", { miner: b.config.userGo });
await crash("mint-after-send", "recover");
let s = await run("recover");
assert.equal(s.backing.supply_atoms, "1000000000");
const original = s.operations.find((o: any) => o.id === wrapId).solSignature;
s = await run("wrap", ["10"], wrapId);
assert.equal(s.backing.supply_atoms, "1000000000");
assert.equal(
  s.operations.find((o: any) => o.id === wrapId).solSignature,
  original,
);
await assert.rejects(
  run("wrap", ["11"], wrapId),
  /different transaction parameters/,
);
report.push(
  "Completed request replay uses original signature; conflicting amount rejected",
);
await crash(
  "burn-after-send",
  "unwrap",
  ["1"],
  "22222222-2222-4222-8222-222222222222",
);
await crash("release-after-send", "recover");
await run("recover");
s = await run("mine", ["3"]);
assert.equal(s.backing.custody_atoms, "900000000");
assert.equal(s.backing.supply_atoms, "900000000");
assert.equal(s.backing.pending_withdrawal_atoms, "0");
s = await run("unwrap", ["1"], "22222222-2222-4222-8222-222222222222");
assert.equal(s.backing.supply_atoms, "900000000");
assert.equal(s.backing.custody_atoms, "900000000");
const release = s.operations.find((o: any) => o.kind === "unwrap").goSignature;
const blocks = await b.go("/chain");
assert.equal(
  blocks
    .flatMap((b: any) => b.txs)
    .filter((tx: any) => tx.signature === release).length,
  1,
);
report.push(
  "Exactly one confirmed release after burn/release crashes; custody and supply both 9",
);
await writeFile(
  `${directory}/verification.json`,
  JSON.stringify({ report, final: s }, null, 2),
);
console.log(JSON.stringify({ directory, report, backing: s.backing }, null, 2));
