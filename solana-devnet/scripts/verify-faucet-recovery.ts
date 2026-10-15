import assert from "node:assert/strict";
import { spawn, ChildProcess } from "node:child_process";
import { readFile, writeFile, access } from "node:fs/promises";
import { resolve, join } from "node:path";
import { Connection, Keypair, PublicKey } from "@solana/web3.js";
import { assertNetwork, Deployment } from "../src/solana";

if (!process.argv.includes("--isolated"))
  throw Error(
    "Use --isolated only in a disposable local deployment. This test interrupts its own faucet process and edits its test journal entry.",
  );
const d: Deployment = JSON.parse(
  await readFile("public/deployment.json", "utf8"),
);
if (d.network !== "localnet")
  throw Error("Recovery verification is localnet-only.");
const c = new Connection(d.rpc, "confirmed");
await assertNetwork(c, d);
const genesis = await c.getGenesisHash();
const address = Keypair.generate().publicKey.toBase58();
const before = await Promise.all(
  [d.mintA, d.mintB].map(
    async (mint) => (await c.getTokenSupply(new PublicKey(mint))).value.amount,
  ),
);
const tag = Date.now().toString();
const marker = resolve(`.local/faucet-recovery-${tag}.marker`);
const configPath = resolve(`.local/faucet-recovery-${tag}.config.ts`);
// Only this generated test config pauses the process; production has no fault
// injection switch. getBalance happens after the durable pending claim.
await writeFile(
  configPath,
  `import config from "../vite.config.ts";
import { Connection } from "@solana/web3.js";
import { writeFile } from "node:fs/promises";
Connection.prototype.getBalance = async function() {
  await writeFile(${JSON.stringify(marker)}, "paused-before-first-funding");
  return new Promise<number>(() => {});
};
export default config;
`,
  { flag: "wx", mode: 0o600 },
);
const url = "http://127.0.0.1:5186";
let output = "";
function launch(config?: string) {
  const child = spawn(
    process.execPath,
    [
      resolve("node_modules/vite/bin/vite.js"),
      "--host",
      "127.0.0.1",
      "--port",
      "5186",
      "--strictPort",
      ...(config ? ["--config", config] : []),
    ],
    {
      cwd: process.cwd(),
      windowsHide: true,
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  for (const stream of [child.stdout, child.stderr])
    stream?.on("data", (bytes) => {
      output = (output + bytes.toString()).slice(-12000);
    });
  return child;
}
async function waitFor(predicate: () => Promise<boolean>, child: ChildProcess) {
  for (let i = 0; i < 150; i++) {
    if (child.exitCode !== null)
      throw Error("Owned test server exited: " + output);
    if (await predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw Error("Owned test server did not reach expected boundary: " + output);
}
async function ready(child: ChildProcess) {
  await waitFor(async () => {
    try {
      return (
        await fetch(url + "/deployment.json", {
          signal: AbortSignal.timeout(300),
        })
      ).ok;
    } catch {
      return false;
    }
  }, child);
}
async function stop(child: ChildProcess) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  child.kill();
  for (
    let i = 0;
    i < 100 && child.exitCode === null && child.signalCode === null;
    i++
  )
    await new Promise((resolve) => setTimeout(resolve, 50));
  if (child.exitCode === null && child.signalCode === null)
    throw Error("Owned test server did not stop; inspect PID " + child.pid);
}
function request(target = address) {
  return fetch(url + "/api/fund", {
    method: "POST",
    headers: { Origin: url, "Content-Type": "application/json" },
    body: JSON.stringify({ address: target }),
    signal: AbortSignal.timeout(20000),
  });
}
let child: ChildProcess | undefined;
try {
  child = launch(configPath);
  await ready(child);
  const interrupted = request().then(
    () => null,
    () => null,
  );
  await waitFor(async () => {
    try {
      await access(marker);
      return true;
    } catch {
      return false;
    }
  }, child);
  await stop(child);
  await interrupted;
  const claimPath = join(
    ".local/faucet-claims/localnet",
    genesis,
    address + ".jsonl",
  );
  const claim = await readFile(claimPath, "utf8");
  assert.equal(JSON.parse(claim.trim().split("\n")[0]).status, "pending");
  // Simulate a lost summary write; the exclusive per-wallet claim still guards
  // replay. Only this newly generated test address is removed from the summary.
  const summaryPath = ".local/localnet-faucet.json";
  const summary = JSON.parse(await readFile(summaryPath, "utf8"));
  delete summary[address];
  await writeFile(summaryPath, JSON.stringify(summary, null, 2), {
    mode: 0o600,
  });
  child = launch();
  await ready(child);
  const retry = await request();
  assert.equal(retry.status, 400);
  assert.match(
    ((await retry.json()) as { error: string }).error,
    /already attempted/,
  );
  assert.equal(await c.getBalance(new PublicKey(address)), 0);
  const after = await Promise.all(
    [d.mintA, d.mintB].map(
      async (mint) =>
        (await c.getTokenSupply(new PublicKey(mint))).value.amount,
    ),
  );
  assert.deepEqual(after, before);
  assert.equal(await readFile(claimPath, "utf8"), claim);
  // Also exercise successful funding with overlapping requests, then remove
  // only its summary entry and prove the completed claim survives a restart.
  const completedAddress = Keypair.generate().publicKey.toBase58();
  const concurrent = await Promise.all([
    request(completedAddress),
    request(completedAddress),
  ]);
  assert.deepEqual(
    concurrent.map((response) => response.status).sort(),
    [200, 400],
  );
  const fundedSupply = await Promise.all(
    [d.mintA, d.mintB].map(
      async (mint) =>
        (await c.getTokenSupply(new PublicKey(mint))).value.amount,
    ),
  );
  for (let i = 0; i < 2; i++) {
    const symbol = i === 0 ? d.symbolA : d.symbolB;
    assert.equal(
      BigInt(fundedSupply[i]) - BigInt(before[i]),
      symbol === "GO" ? 200000000n : 20000000000n,
    );
  }
  const completedPath = join(
    ".local/faucet-claims/localnet",
    genesis,
    completedAddress + ".jsonl",
  );
  const completedLog = await readFile(completedPath, "utf8");
  assert.equal(
    JSON.parse(completedLog.trim().split("\n").at(-1)!).status,
    "completed",
  );
  await stop(child);
  const completedSummary = JSON.parse(await readFile(summaryPath, "utf8"));
  delete completedSummary[completedAddress];
  await writeFile(summaryPath, JSON.stringify(completedSummary, null, 2), {
    mode: 0o600,
  });
  child = launch();
  await ready(child);
  const repeatedFunding = await request(completedAddress);
  assert.equal(repeatedFunding.status, 400);
  assert.match(
    ((await repeatedFunding.json()) as { error: string }).error,
    /already attempted/,
  );
  assert.equal(await readFile(completedPath, "utf8"), completedLog);
  assert.deepEqual(
    await Promise.all(
      [d.mintA, d.mintB].map(
        async (mint) =>
          (await c.getTokenSupply(new PublicKey(mint))).value.amount,
      ),
    ),
    fundedSupply,
  );
  const report = {
    network: d.network,
    genesis,
    address,
    result:
      "Interrupted claim replay rejected with no issuance; overlapping successful requests funded exactly once; completed claim replay rejected after restart and summary loss",
    completedAddress,
  };
  await writeFile(
    ".local/faucet-recovery-verification.json",
    JSON.stringify(report, null, 2),
  );
  console.log(JSON.stringify(report, null, 2));
} finally {
  if (child) await stop(child);
}
