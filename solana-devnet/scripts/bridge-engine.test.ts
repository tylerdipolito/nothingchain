import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Keypair, SystemProgram } from "@solana/web3.js";
import { Bridge } from "./bridge";
import { Journal, Operation } from "./bridge-core";

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
