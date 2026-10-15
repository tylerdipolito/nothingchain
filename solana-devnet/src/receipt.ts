import type { Connection } from "@solana/web3.js";
export interface Receipt {
  version: 1;
  signature: string;
  lastValidBlockHeight: number;
  label: string;
  state:
    | "ready"
    | "pending"
    | "unknown"
    | "confirmed"
    | "failed"
    | "expired"
    | "not-sent";
}
export function parseReceipt(input: string): Receipt {
  if (input.length > 2048)
    throw Error(
      "Stored transaction receipt is invalid; inspect browser storage before trading.",
    );
  const r = JSON.parse(input) as Receipt;
  if (
    !r ||
    r.version !== 1 ||
    typeof r.signature !== "string" ||
    !/^[1-9A-HJ-NP-Za-km-z]{64,88}$/.test(r.signature) ||
    !Number.isSafeInteger(r.lastValidBlockHeight) ||
    r.lastValidBlockHeight < 0 ||
    typeof r.label !== "string" ||
    r.label.length > 200 ||
    ![
      "ready",
      "pending",
      "unknown",
      "confirmed",
      "failed",
      "expired",
      "not-sent",
    ].includes(r.state)
  )
    throw Error(
      "Stored transaction receipt is invalid; inspect browser storage before trading.",
    );
  return r;
}
export function receiptPending(r: Receipt | null) {
  return !!r && ["ready", "pending", "unknown"].includes(r.state);
}
export async function reconcileReceipt(
  c: Pick<Connection, "getSignatureStatuses" | "getBlockHeight">,
  r: Receipt,
): Promise<Receipt["state"]> {
  const read = async () =>
    (
      await c.getSignatureStatuses([r.signature], {
        searchTransactionHistory: true,
      })
    ).value[0];
  let status = await read();
  const resolved = () =>
    status &&
    ["confirmed", "finalized"].includes(status.confirmationStatus || "")
      ? status.err
        ? "failed"
        : "confirmed"
      : null;
  const initial = resolved();
  if (initial) return initial;
  if (
    !status &&
    (await c.getBlockHeight("confirmed")) > r.lastValidBlockHeight
  ) {
    // Re-read after observing expiry, so an inclusion racing the first lookup
    // cannot be classified as absent using the earlier snapshot.
    status = await read();
    return resolved() || (status ? "pending" : "expired");
  }
  return "pending";
}
