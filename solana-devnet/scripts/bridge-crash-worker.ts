// Test-only process instrumentation. The production server has no pause/kill switch.
import { writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { Bridge, runBridge } from "./bridge";
const [flag, directory, phase, command, id, ...args] = process.argv.slice(2);
if (
  flag !== "--isolated" ||
  !resolve(directory).includes(
    `${process.platform === "win32" ? "\\" : "/"}.local${process.platform === "win32" ? "\\" : "/"}bridge-crash-`,
  )
)
  throw Error("Disposable crash directory required");
async function pause() {
  await writeFile(`${directory}/paused.marker`, phase, { flag: "wx" });
  setInterval(() => {}, 1000);
  await new Promise(() => {});
}
const load = Bridge.prototype.load;
Bridge.prototype.load = async function () {
  await load.call(this);
  const send = this.c.sendRawTransaction.bind(this.c);
  this.c.sendRawTransaction = async (...params) => {
    const signature = await send(...params);
    const kind = this.journal.state.operations.at(-1)?.kind;
    if (
      (phase === "mint-after-send" && kind === "wrap") ||
      (phase === "burn-after-send" && kind === "unwrap")
    )
      await pause();
    return signature;
  };
};
const go = Bridge.prototype.go;
Bridge.prototype.go = async function (path, body: any) {
  if (
    path === "/tx" &&
    phase === "deposit-before-send" &&
    body.memo.startsWith("bridge-wrap:")
  )
    await pause();
  const result = await go.call(this, path, body);
  if (
    path === "/tx" &&
    phase === "release-after-send" &&
    body.memo.startsWith("bridge-release:")
  )
    await pause();
  return result;
};
await runBridge(directory, command, args, id === "-" ? undefined : id);
