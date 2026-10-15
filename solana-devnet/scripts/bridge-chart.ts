import { createHash } from "node:crypto";
import { readFile, open } from "node:fs/promises";
import { Connection, PublicKey } from "@solana/web3.js";
import { utils } from "@coral-xyz/anchor";
import { PROGRAM, poolAddresses } from "../src/solana";

export interface Trade {
  signature: string;
  slot: number;
  order: number;
  time: number;
  side: "buy" | "sell";
  goAtoms: string;
  usdAtoms: string;
}
const swapDiscriminator = createHash("sha256")
  .update("global:swap")
  .digest()
  .subarray(0, 8);
function ratio(t: Trade) {
  const scale = 1000000000000n,
    n = (BigInt(t.usdAtoms) * scale) / BigInt(t.goAtoms);
  return `${n / scale}.${(n % scale).toString().padStart(12, "0")}`;
}
export function candles(trades: Trade[], seconds: number) {
  if (![60, 300, 3600].includes(seconds))
    throw Error("Unsupported candle interval");
  const sorted = [...trades].sort(
    (a, b) => a.slot - b.slot || a.order - b.order,
  );
  const groups = new Map<number, Trade[]>();
  for (const t of sorted) {
    const time = Math.floor(t.time / seconds) * seconds;
    groups.set(time, [...(groups.get(time) || []), t]);
  }
  return [...groups.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([time, list]) => {
      const less = (a: Trade, b: Trade) =>
        BigInt(a.usdAtoms) * BigInt(b.goAtoms) <
        BigInt(b.usdAtoms) * BigInt(a.goAtoms);
      const low = list.reduce((a, b) => (less(a, b) ? a : b)),
        high = list.reduce((a, b) => (less(a, b) ? b : a));
      return {
        time,
        open: ratio(list[0]),
        high: ratio(high),
        low: ratio(low),
        close: ratio(list.at(-1)!),
        goVolumeAtoms: list
          .reduce((n, t) => n + BigInt(t.goAtoms), 0n)
          .toString(),
        usdVolumeAtoms: list
          .reduce((n, t) => n + BigInt(t.usdAtoms), 0n)
          .toString(),
        trades: list.length,
      };
    });
}
// Only one direct swap of our pool per transaction is charted. Reserve deltas
// must match that signed instruction, excluding deposits/donations/failed swaps.
export function decodeTrade(
  tx: any,
  signature: string,
  pool: string,
  vaultA: string,
  vaultB: string,
  mintA: string,
  goMint: string,
  usdMint: string,
  order: number,
): Trade | null {
  if (!tx || !tx.meta || tx.meta.err || !Number.isInteger(tx.blockTime))
    return null;
  const message = tx.transaction.message;
  const keys = [
    ...(message.accountKeys || message.staticAccountKeys),
    ...(tx.meta.loadedAddresses?.writable || []),
    ...(tx.meta.loadedAddresses?.readonly || []),
  ].map((k) => k.toString());
  const instructions = message.instructions || message.compiledInstructions;
  const own = instructions.filter(
    (i: any) => keys[i.programIdIndex] === PROGRAM.toBase58(),
  );
  if (own.length !== 1) return null;
  const ix = own[0],
    data =
      typeof ix.data === "string"
        ? Buffer.from(utils.bytes.bs58.decode(ix.data))
        : Buffer.from(ix.data);
  const accounts = Array.from(ix.accounts || ix.accountKeyIndexes) as number[];
  if (
    data.length !== 33 ||
    !data.subarray(0, 8).equals(swapDiscriminator) ||
    ![0, 1].includes(data[8]) ||
    keys[accounts[1]] !== pool ||
    keys[accounts[2]] !== vaultA ||
    keys[accounts[3]] !== vaultB
  )
    return null;
  const input = data.readBigUInt64LE(9),
    min = data.readBigUInt64LE(17),
    aToB = data[8] === 1;
  const delta = (vault: string, mint: string) => {
    const index = keys.indexOf(vault);
    const pre = tx.meta.preTokenBalances?.find(
        (b: any) => b.accountIndex === index && b.mint === mint,
      ),
      post = tx.meta.postTokenBalances?.find(
        (b: any) => b.accountIndex === index && b.mint === mint,
      );
    if (
      !pre ||
      !post ||
      pre.uiTokenAmount.decimals !== 8 ||
      post.uiTokenAmount.decimals !== 8
    )
      throw Error("Missing eight-decimal vault balances");
    return BigInt(post.uiTokenAmount.amount) - BigInt(pre.uiTokenAmount.amount);
  };
  try {
    const goVault = mintA === goMint ? vaultA : vaultB,
      usdVault = mintA === usdMint ? vaultA : vaultB;
    const go = delta(goVault, goMint),
      usd = delta(usdVault, usdMint);
    const buy =
      (aToB ? mintA : mintA === goMint ? usdMint : goMint) === usdMint;
    if (
      buy
        ? go >= 0n || usd !== input || -go < min
        : usd >= 0n || go !== input || -usd < min
    )
      return null;
    return {
      signature,
      slot: tx.slot,
      order,
      time: tx.blockTime,
      side: buy ? "buy" : "sell",
      goAtoms: (go < 0n ? -go : go).toString(),
      usdAtoms: (usd < 0n ? -usd : usd).toString(),
    };
  } catch {
    return null;
  }
}
export async function chartHistory(
  c: Connection,
  directory: string,
  genesis: string,
  goMint: string,
  usdMint: string,
) {
  const sorted = [new PublicKey(goMint), new PublicKey(usdMint)].sort((a, b) =>
    Buffer.compare(a.toBuffer(), b.toBuffer()),
  );
  const addresses = poolAddresses(sorted[0], sorted[1]),
    pool = addresses.pool.toBase58(),
    path = `${directory}/trades.jsonl`;
  const seen = new Set<string>(),
    trades: Trade[] = [];
  try {
    const raw = await readFile(path, "utf8");
    if (raw && !raw.endsWith("\n"))
      throw Error("Chart cache incomplete; preserve it for inspection");
    for (const line of raw.trim().split("\n").filter(Boolean)) {
      const row = JSON.parse(line);
      if (row.genesis !== genesis || row.pool !== pool)
        throw Error("Chart cache network mismatch");
      if (!seen.has(row.signature)) {
        seen.add(row.signature);
        if (row.trade) trades.push(row.trade);
      }
    }
  } catch (e: any) {
    if (e.code !== "ENOENT") throw e;
  }
  const signatures = await c.getSignaturesForAddress(
    addresses.pool,
    { limit: 100 },
    "finalized",
  );
  const missing = signatures
    .filter((s) => !seen.has(s.signature) && !s.err)
    .reverse();
  let unavailable = 0;
  for (const s of missing.slice(0, 25)) {
    const tx = await c.getTransaction(s.signature, {
      commitment: "finalized",
      maxSupportedTransactionVersion: 0,
    });
    if (!tx) {
      unavailable++;
      continue;
    }
    const block = await c
      .getBlockSignatures(s.slot, "finalized")
      .catch(() => null);
    const order = block?.signatures.indexOf(s.signature) ?? -1;
    if (order < 0) {
      unavailable++;
      continue;
    }
    const trade = decodeTrade(
      tx,
      s.signature,
      pool,
      addresses.vaultA.toBase58(),
      addresses.vaultB.toBase58(),
      sorted[0].toBase58(),
      goMint,
      usdMint,
      order,
    );
    const file = await open(path, "a", 0o600);
    try {
      await file.writeFile(
        JSON.stringify({ genesis, pool, signature: s.signature, trade }) + "\n",
      );
      await file.sync();
    } finally {
      await file.close();
    }
    seen.add(s.signature);
    if (trade) trades.push(trade);
  }
  const ordered = trades.sort((a, b) => a.slot - b.slot || a.order - b.order);
  return {
    notice:
      "Finalized swaps captured from this pool. Older pruned history may be unavailable. Empty intervals have no candles. Execution prices include the input fee.",
    unavailable,
    backlog: Math.max(0, missing.length - 25),
    tradeCount: ordered.length,
    candles: {
      "1m": candles(ordered, 60).slice(-120),
      "5m": candles(ordered, 300).slice(-120),
      "1h": candles(ordered, 3600).slice(-120),
    },
    recent: ordered
      .slice(-20)
      .reverse()
      .map((t) => ({ ...t, price: ratio(t) })),
  };
}
