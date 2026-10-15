import test from "node:test";
import assert from "node:assert/strict";
import { Keypair, Transaction, SystemProgram } from "@solana/web3.js";
import { utils } from "@coral-xyz/anchor";
import { keyWallet } from "./solana";
import {
  submitTransaction,
  SubmitConnection,
  TransactionUpdate,
} from "./submit";

function fixture(mode: "success" | "timeout" | "failed") {
  const key = Keypair.generate();
  let broadcasts = 0;
  let signature = "";
  const tx = new Transaction().add(
    SystemProgram.transfer({
      fromPubkey: key.publicKey,
      toPubkey: Keypair.generate().publicKey,
      lamports: 0,
    }),
  );
  const c = {
    async getLatestBlockhash() {
      return {
        blockhash: Keypair.generate().publicKey.toBase58(),
        lastValidBlockHeight: 100,
      };
    },
    async sendRawTransaction(raw: Uint8Array) {
      broadcasts++;
      signature = utils.bytes.bs58.encode(Transaction.from(raw).signature!);
      return signature;
    },
    async confirmTransaction() {
      if (mode === "timeout") throw Error("connection lost after broadcast");
      return {
        value: {
          err:
            mode === "failed"
              ? { InstructionError: [0, "test failure"] }
              : null,
        },
      };
    },
  } as unknown as SubmitConnection;
  return { c, tx, wallet: keyWallet(key), broadcasts: () => broadcasts };
}
test("submission reports wallet, pending and confirmed states with the signed receipt", async () => {
  const f = fixture("success"),
    events: TransactionUpdate[] = [];
  const signature = await submitTransaction(f.c, f.wallet, f.tx, (e) =>
    events.push(e),
  );
  assert.deepEqual(
    events.map((e) => e.state),
    ["preparing", "signing", "ready", "pending", "confirmed"],
  );
  assert.equal(events[2].signature, signature);
  assert.equal(events[2].lastValidBlockHeight, 100);
  assert.equal(f.broadcasts(), 1);
});
test("lost confirmation is uncertain and never rebroadcast automatically", async () => {
  const f = fixture("timeout"),
    events: TransactionUpdate[] = [];
  await assert.rejects(
    submitTransaction(f.c, f.wallet, f.tx, (e) => events.push(e)),
    /outcome is unconfirmed/,
  );
  assert.equal(events.at(-1)?.state, "unknown");
  assert(events.at(-1)?.signature);
  assert.equal(f.broadcasts(), 1);
});
test("definitive on-chain failure is never labeled confirmed", async () => {
  const f = fixture("failed"),
    events: TransactionUpdate[] = [];
  await assert.rejects(
    submitTransaction(f.c, f.wallet, f.tx, (e) => events.push(e)),
    /failed on chain/,
  );
  assert.equal(events.at(-1)?.state, "failed");
  assert(!events.some((e) => e.state === "confirmed"));
});
test("receipt persistence failure or wallet rejection prevents broadcast", async () => {
  const f = fixture("success");
  const rejectedEvents: TransactionUpdate[] = [];
  await assert.rejects(
    submitTransaction(f.c, f.wallet, f.tx, (e) => {
      rejectedEvents.push(e);
      if (e.state === "ready") throw Error("storage unavailable");
    }),
    /storage unavailable/,
  );
  assert.equal(f.broadcasts(), 0);
  assert.equal(rejectedEvents.at(-1)?.state, "not-sent");
  assert.equal(
    rejectedEvents.at(-1)?.signature,
    undefined,
    "a refused receipt must not resolve an existing pending signature",
  );
  const second = fixture("success");
  second.wallet.signTransaction = async () => {
    throw Error("User cancelled");
  };
  await assert.rejects(
    submitTransaction(second.c, second.wallet, second.tx),
    /User cancelled/,
  );
  assert.equal(second.broadcasts(), 0);
});
