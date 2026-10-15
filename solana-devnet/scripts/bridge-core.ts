import {
  createHash,
  createPrivateKey,
  createPublicKey,
  randomBytes,
  sign,
} from "node:crypto";
import { mkdir, open, readFile, unlink } from "node:fs/promises";

export const ATOMS = 100000000n;
export function amount(value: string): bigint {
  if (!/^(0|[1-9]\d*)(\.\d{1,8})?$/.test(value))
    throw Error("Use decimal digits, up to eight decimal places");
  const [whole, fraction = ""] = value.split(".");
  const n = BigInt(whole) * ATOMS + BigInt(fraction.padEnd(8, "0"));
  if (n <= 0n || n > BigInt(Number.MAX_SAFE_INTEGER))
    throw Error("Amount outside safe bridge limits");
  return n;
}
export function display(n: bigint) {
  return `${n / ATOMS}.${(n % ATOMS).toString().padStart(8, "0")}`;
}
export function localURL(value: string) {
  const u = new URL(value);
  if (
    u.protocol !== "http:" ||
    u.hostname !== "127.0.0.1" ||
    u.username ||
    u.password ||
    u.pathname !== "/" ||
    u.search ||
    u.hash
  )
    throw Error(
      "Bridge only accepts plain http://127.0.0.1:PORT local endpoints",
    );
  return u.origin;
}
export function goWallet(seed = randomBytes(32).toString("hex")) {
  if (!/^[a-f0-9]{64}$/.test(seed)) throw Error("Invalid Go wallet seed");
  const privateKey = createPrivateKey({
    key: Buffer.concat([
      Buffer.from("302e020100300506032b657004220420", "hex"),
      Buffer.from(seed, "hex"),
    ]),
    format: "der",
    type: "pkcs8",
  });
  const public_key = createPublicKey(privateKey)
    .export({ format: "der", type: "spki" })
    .subarray(-32)
    .toString("hex");
  return {
    seed,
    public_key,
    address:
      "go1" +
      createHash("sha256").update(Buffer.from(public_key, "hex")).digest("hex"),
  };
}
export type GoWallet = ReturnType<typeof goWallet>;
export function goSign(
  wallet: GoWallet,
  chain: string,
  to: string,
  atoms: bigint,
  memo: string,
  nonce: number,
) {
  if (
    !/^go1[a-f0-9]{64}$/.test(to) ||
    !Number.isSafeInteger(nonce) ||
    nonce < 1 ||
    atoms <= 0n ||
    atoms > BigInt(Number.MAX_SAFE_INTEGER)
  )
    throw Error("Invalid transfer");
  if (
    !/^[a-z0-9-]+$/.test(chain) ||
    !/^[\x20-\x7e]{0,512}$/.test(memo) ||
    /[<>&]/.test(memo)
  )
    throw Error(
      "Bridge signing supports ASCII memos without HTML escapes only",
    );
  const key = createPrivateKey({
    key: Buffer.concat([
      Buffer.from("302e020100300506032b657004220420", "hex"),
      Buffer.from(wallet.seed, "hex"),
    ]),
    format: "der",
    type: "pkcs8",
  });
  const payload = {
    chain,
    from: wallet.address,
    to,
    amount_atoms: Number(atoms),
    memo,
    nonce,
  };
  // All fields are restricted to ASCII here, matching Go's encoding/json signing bytes.
  const signature = sign(
    null,
    Buffer.from(JSON.stringify(payload)),
    key,
  ).toString("hex");
  const { chain: _, ...fields } = payload;
  return { ...fields, public_key: wallet.public_key, signature };
}
export type GoTx = ReturnType<typeof goSign>;
export interface Block {
  index: number;
  hash: string;
  prev_hash: string;
  txs: GoTx[];
}
export function findTransfer(blocks: Block[], tx: GoTx, confirmations: number) {
  const block = blocks.find((b) =>
    b.txs.some(
      (t) =>
        t.signature === tx.signature &&
        JSON.stringify(t) === JSON.stringify(tx),
    ),
  );
  if (!block || blocks.length - block.index < confirmations) return undefined;
  return { height: block.index, hash: block.hash };
}
export interface Operation {
  id: string;
  requestHash?: string;
  kind: "wrap" | "unwrap" | "setup" | "pool" | "swap";
  atoms?: string;
  go?: GoTx;
  anchor?: { height: number; hash: string };
  sol?: {
    raw: string;
    signature: string;
    expiry: number;
    finalizedSlot?: number;
  };
  solConfirmed?: boolean;
  done?: boolean;
}
export interface BridgeState {
  operations: Operation[];
}
// Append-only full snapshots. fsync before broadcasting; torn/corrupt logs fail closed.
export class Journal {
  state: BridgeState = { operations: [] };
  constructor(readonly directory: string) {}
  async load() {
    try {
      const raw = await readFile(`${this.directory}/journal.jsonl`, "utf8");
      if (!raw.endsWith("\n"))
        throw Error(
          "Torn bridge journal: reconcile manually; never delete to retry",
        );
      for (const line of raw.trimEnd().split("\n")) {
        const entry = JSON.parse(line);
        if (
          entry.hash !==
          createHash("sha256").update(JSON.stringify(entry.state)).digest("hex")
        )
          throw Error("Bridge journal checksum mismatch");
        this.state = entry.state;
      }
    } catch (e: any) {
      if (e.code !== "ENOENT") throw e;
    }
    return this.state;
  }
  async save() {
    const file = await open(`${this.directory}/journal.jsonl`, "a", 0o600);
    try {
      await file.writeFile(
        JSON.stringify({
          state: this.state,
          hash: createHash("sha256")
            .update(JSON.stringify(this.state))
            .digest("hex"),
        }) + "\n",
      );
      await file.sync();
    } finally {
      await file.close();
    }
  }
}
export async function exclusive<T>(directory: string, work: () => Promise<T>) {
  await mkdir(directory, { recursive: true });
  const path = `${directory}/operator.lock`;
  const lock = await open(path, "wx", 0o600).catch(() => {
    throw Error(
      "Bridge busy or stale operator.lock. Verify the previous process stopped before manual recovery.",
    );
  });
  try {
    await lock.writeFile(String(process.pid));
    await lock.sync();
    return await work();
  } finally {
    await lock.close();
    await unlink(path);
  }
}
export function checkAnchors(operations: Operation[], blocks: Block[]) {
  for (const op of operations)
    if (op.anchor && blocks[op.anchor.height]?.hash !== op.anchor.hash)
      throw Error(
        `BRIDGE HALTED: GoChain reorganization removed ${op.id}. Operator reconciliation required.`,
      );
}
export function checkFinalizedReceipt(
  observed: number | undefined,
  firstAvailable: number,
  tip: number,
  status: { slot: number; err: unknown; confirmationStatus?: string } | null,
) {
  if (!Number.isSafeInteger(observed) || observed! > tip)
    throw Error(
      "BRIDGE HALTED: missing finalized receipt or Solana ledger rolled back",
    );
  if (!status && observed! < firstAvailable) return; // Trust previously observed finality after explicit pruning.
  if (
    !status ||
    status.err ||
    status.confirmationStatus !== "finalized" ||
    status.slot !== observed
  )
    throw Error(
      "BRIDGE HALTED: previously finalized Solana history is unavailable or changed",
    );
}
export function backing(
  custody: bigint,
  supply: bigint,
  operations: Operation[],
) {
  const owed = operations
    .filter((o) => o.kind === "unwrap" && o.solConfirmed && !o.done)
    .reduce((n, o) => n + BigInt(o.atoms!), 0n);
  // A submitted release may already be confirmed but not journaled. Count it until reconciled;
  // callers reconcile pending releases before performing this conservative check.
  if (custody < supply + owed)
    throw Error(
      "BRIDGE HALTED: custody below wGO supply plus pending withdrawals",
    );
  return {
    custody_atoms: custody.toString(),
    supply_atoms: supply.toString(),
    pending_withdrawal_atoms: owed.toString(),
    surplus_atoms: (custody - supply - owed).toString(),
  };
}
