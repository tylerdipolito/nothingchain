import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Keypair, SystemProgram } from "@solana/web3.js";
import { Bridge } from "./bridge";
import { Journal, Operation } from "./bridge-core";

test("expired swaps preserve an explicitly unknown outcome; finalized failures do not count as successful swaps", async () => {
  const dir = await mkdtemp(join(tmpdir(), "bridge-failed-swap-"));
  try {
    const b = new Bridge(dir),
      op: Operation = {
        id: "failed-swap",
        kind: "swap",
        sol: { raw: "unused", signature: "sig", expiry: 10 },
      };
    b.journal.state.operations.push(op);
    let sends = 0;
    b.c = {
      getSignatureStatuses: async () => ({ value: [null] }),
      getBlockHeight: async () => 11,
      sendRawTransaction: async () => {
        sends++;
        throw Error("must not resend expired swap");
      },
    } as any;
    await assert.rejects(b.confirmSol(op), /cannot verify whether it executed/);
    assert(op.done);
    assert.equal(op.solConfirmed, undefined);
    assert.equal(sends, 0);
    const reloaded = new Journal(dir);
    await reloaded.load();
    assert(reloaded.state.operations[0].failure?.includes("No replacement"));
    op.done = false;
    op.failure = undefined;
    b.c = {
      getSignatureStatuses: async () => ({
        value: [
          {
            confirmationStatus: "finalized",
            err: { InstructionError: [0, { Custom: 6002 }] },
          },
        ],
      }),
    } as any;
    await assert.rejects(b.confirmSol(op), /failed on chain/);
    assert(op.done);
    assert.equal(op.solConfirmed, undefined);
  } finally {
    await rm(dir, { recursive: true });
  }
});

test("signed Solana bytes survive restart; finalized recovery never broadcasts twice", async () => {
  const dir = await mkdtemp(join(tmpdir(), "bridge-engine-"));
  try {
    const b = new Bridge(dir),
      payer = Keypair.generate();
    const op: Operation = { id: "crash-boundary", kind: "unwrap", atoms: "1" };
    b.journal.state.operations.push(op);
    b.c = {
      getLatestBlockhash: async () => ({
        blockhash: Keypair.generate().publicKey.toBase58(),
        lastValidBlockHeight: 100,
      }),
    } as any;
    await b.prepareSol(
      op,
      [
        SystemProgram.transfer({
          fromPubkey: payer.publicKey,
          toPubkey: Keypair.generate().publicKey,
          lamports: 1,
        }),
      ],
      payer,
    );
    const reopened = new Bridge(dir);
    reopened.journal = new Journal(dir);
    await reopened.journal.load();
    const recovered = reopened.journal.state.operations[0];
    assert.deepEqual(recovered.sol, op.sol);
    let broadcast = 0,
      confirmed = false;
    reopened.c = {
      getSignatureStatuses: async () => ({
        value: [
          confirmed
            ? { confirmationStatus: "finalized", err: null, slot: 42 }
            : null,
        ],
      }),
      getBlockHeight: async () => 50,
      sendRawTransaction: async (raw: Buffer) => {
        broadcast++;
        assert.equal(raw.toString("base64"), op.sol!.raw);
        return op.sol!.signature;
      },
      confirmTransaction: async () => {
        confirmed = true;
        return { value: { err: null } };
      },
    } as any;
    await reopened.confirmSol(recovered);
    assert.equal(broadcast, 1);
    recovered.solConfirmed = false;
    await reopened.confirmSol(recovered);
    assert.equal(broadcast, 1);
    recovered.solConfirmed = false;
    reopened.c = {
      getSignatureStatuses: async () => ({
        value: [
          {
            confirmationStatus: "finalized",
            err: { InstructionError: [0, "InvalidArgument"] },
          },
        ],
      }),
    } as any;
    await assert.rejects(reopened.confirmSol(recovered), /failed/);
    reopened.c = {
      getSignatureStatuses: async () => ({ value: [null] }),
      getBlockHeight: async () => 101,
      sendRawTransaction: async () => {
        throw Error("must not broadcast expired intent");
      },
    } as any;
    await assert.rejects(reopened.confirmSol(recovered), /Expired\/unknown/);
    assert.equal(broadcast, 1);
  } finally {
    await rm(dir, { recursive: true });
  }
});
