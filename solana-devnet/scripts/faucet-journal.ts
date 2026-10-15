import { mkdir, open } from "node:fs/promises";
import type { FileHandle } from "node:fs/promises";
import { join } from "node:path";

// Existence is the replay guard, even if a crash leaves a partial final line.
// Never automatically remove a claim after an ambiguous network outcome.
export async function claimFunding(
  base: string,
  network: string,
  genesis: string,
  address: string,
) {
  if (
    !["localnet", "devnet"].includes(network) ||
    ![genesis, address].every((value) =>
      /^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(value),
    )
  )
    throw Error("Invalid faucet claim identity.");
  const directory = join(base, network, genesis);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  let file: FileHandle;
  try {
    file = await open(join(directory, address + ".jsonl"), "wx", 0o600);
  } catch (e: any) {
    if (e.code === "EEXIST")
      throw Error(
        "Faucet already attempted for this wallet. Inspect claim journal before retrying.",
      );
    throw e;
  }
  async function record(event: Record<string, unknown>) {
    await file.appendFile(JSON.stringify(event) + "\n");
    await file.sync();
  }
  try {
    await record({
      status: "pending",
      network,
      genesis,
      address,
      time: new Date().toISOString(),
    });
  } catch (e) {
    await file.close();
    throw e;
  }
  return { record, close: () => file.close() };
}
