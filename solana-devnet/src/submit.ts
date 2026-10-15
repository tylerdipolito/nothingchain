import { Connection, Transaction } from "@solana/web3.js";
import { utils } from "@coral-xyz/anchor";
import type { Wallet } from "./solana";

export type TransactionState =
  | "preparing"
  | "signing"
  | "ready"
  | "pending"
  | "confirmed"
  | "failed"
  | "unknown"
  | "not-sent";
export interface TransactionUpdate {
  state: TransactionState;
  signature?: string;
  lastValidBlockHeight?: number;
}
export type SubmitConnection = Pick<
  Connection,
  "getLatestBlockhash" | "sendRawTransaction" | "confirmTransaction"
>;

// One signed transaction, one broadcast call. Confirmation errors never trigger
// a new signature/broadcast. The ready callback can persist a recovery receipt;
// if persistence fails, it throws before any network submission.
export async function submitTransaction(
  c: SubmitConnection,
  wallet: Wallet,
  tx: Transaction,
  update: (event: TransactionUpdate) => void = () => {},
) {
  let broadcast = false;
  let prepared = false;
  let receipt: Omit<TransactionUpdate, "state"> = {};
  try {
    update({ state: "preparing" });
    const latest = await c.getLatestBlockhash("confirmed");
    tx.feePayer = wallet.publicKey;
    tx.recentBlockhash = latest.blockhash;
    update({ state: "signing" });
    const signed = await wallet.signTransaction(tx);
    const raw = signed.serialize();
    if (!signed.signature)
      throw Error("Wallet returned no transaction signature.");
    receipt = {
      signature: utils.bytes.bs58.encode(signed.signature),
      lastValidBlockHeight: latest.lastValidBlockHeight,
    };
    update({ state: "ready", ...receipt });
    prepared = true;
    broadcast = true;
    const signature = await c.sendRawTransaction(raw, {
      skipPreflight: false,
      preflightCommitment: "confirmed",
      maxRetries: 0,
    });
    if (signature !== receipt.signature)
      throw Error("RPC returned an unexpected transaction signature.");
    update({ state: "pending", ...receipt });
    const confirmation = await c.confirmTransaction(
      { ...latest, signature },
      "confirmed",
    );
    if (confirmation.value.err) {
      update({ state: "failed", ...receipt });
      throw new ConfirmedFailure(
        "Transaction failed on chain: " +
          JSON.stringify(confirmation.value.err),
      );
    }
    update({ state: "confirmed", ...receipt });
    return signature;
  } catch (e) {
    if (!(e instanceof ConfirmedFailure))
      update({
        state: broadcast ? "unknown" : "not-sent",
        ...(prepared ? receipt : {}),
      });
    if (broadcast && !(e instanceof ConfirmedFailure))
      throw Error(
        `Transaction outcome is unconfirmed. Check ${receipt.signature} before submitting another trade. No automatic retry was made.`,
      );
    throw e;
  }
}
class ConfirmedFailure extends Error {}
