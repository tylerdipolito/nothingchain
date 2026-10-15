import assert from "node:assert/strict";
import { spawn, ChildProcess } from "node:child_process";
import { resolve } from "node:path";
import { runBridge } from "./bridge";
if (process.argv[2] !== "--isolated" || !process.argv[3])
  throw Error(
    "Use --isolated .local/bridge-crash-TIMESTAMP after the crash suite",
  );
const directory = resolve(process.argv[3]);
if (
  !directory.startsWith(resolve(".local") + "/bridge-crash-") &&
  !directory.startsWith(resolve(".local") + "\\bridge-crash-")
)
  throw Error("Disposable crash test directory required");
const url = "http://127.0.0.1:5182";
let child: ChildProcess | undefined;
async function launch() {
  let logs = "";
  child = spawn(
    process.execPath,
    [
      "--import",
      "tsx",
      resolve("scripts/bridge-server.ts"),
      "--dir",
      directory,
      "--port",
      "5182",
    ],
    { windowsHide: true, stdio: ["ignore", "pipe", "pipe"] },
  );
  child.stderr!.on("data", (b) => (logs += b));
  child.stdout!.on("data", (b) => (logs += b));
  for (let i = 0; i < 150; i++) {
    if (child.exitCode !== null) throw Error(logs);
    if (logs.includes("Local trusted bridge UI:")) {
      const html = await (await fetch(url)).text();
      const token = html.match(/const token\s*=\s*["']([a-f0-9]{64})["']/)?.[1];
      assert(token);
      return token;
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw Error("Test HTTP server not ready: " + logs);
}
async function stop() {
  if (child && child.exitCode === null && child.signalCode === null) {
    const ended = new Promise((r) => child!.once("exit", r));
    child.kill("SIGKILL");
    await ended;
  }
}
const post = async (
  token: string,
  command: string,
  args: string[] = [],
  requestId?: string,
) => {
  const r = await fetch(url + "/api", {
    method: "POST",
    headers: {
      Origin: url,
      "Content-Type": "application/json",
      "X-Bridge-Token": token,
    },
    body: JSON.stringify({ command, args, requestId }),
  });
  return { status: r.status, body: await r.json() };
};
try {
  const before: any = await runBridge(directory, "status", []);
  assert(
    before.operations.every((o: any) => o.done),
    "Finish browser pending operations before HTTP suite",
  );
  let token = await launch();
  const id = "11111111-1111-4111-8111-111111111111";
  assert.equal((await post(token, "wrap", ["10"])).status, 400);
  assert.equal((await post("wrong", "status")).status, 400);
  const replay = await post(token, "wrap", ["10"], id);
  assert.equal(replay.status, 200);
  assert.deepEqual(replay.body.backing, before.backing);
  const conflict = await post(token, "wrap", ["11"], id);
  assert.equal(conflict.status, 400);
  assert.equal(conflict.body.safeToClear, false);
  const invalid = await post(
    token,
    "wrap",
    ["0"],
    "33333333-3333-4333-8333-333333333333",
  );
  assert.equal(invalid.status, 400);
  assert.equal(invalid.body.safeToClear, true);
  const concurrent = await Promise.all([
    post(token, "wrap", ["10"], id),
    post(token, "wrap", ["10"], id),
  ]);
  assert(concurrent.some((r) => r.status === 200));
  assert(concurrent.every((r) => [200, 409].includes(r.status)));
  const old = token;
  await stop();
  token = await launch();
  assert.notEqual(old, token);
  assert.equal((await post(old, "status")).status, 400);
  assert.equal((await post(token, "wrap", ["10"], id)).status, 200);
  const after: any = await runBridge(directory, "status", []);
  assert.deepEqual(after.backing, before.backing);
  assert.equal(after.operations.length, before.operations.length);
  console.log(
    "HTTP PASS: IDs required; exact/conflicting/concurrent replay; safe validation failure; restart with fresh token; no extra issuance or operations",
  );
} finally {
  await stop();
}
