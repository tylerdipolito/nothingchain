import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, appendFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createPublicKey, verify } from "node:crypto";
import {
  amount,
  backing,
  checkAnchors,
  checkFinalizedReceipt,
  exclusive,
  findTransfer,
  goSign,
  goWallet,
  Journal,
  localURL,
  Operation,
} from "./bridge-core";

test("bridge preserves eight decimals and rejects rounding/unsafe input", () => {
  assert.equal(amount("0.00000001"), 1n);
  assert.equal(amount("12.34567891"), 1234567891n);
  for (const input of [
    "0",
    "-1",
    "NaN",
    "1e2",
    "1.000000001",
    "90071992.54740992",
    "01",
    "",
  ])
    assert.throws(() => amount(input));
  for (const url of [
    "https://127.0.0.1:8897",
    "http://example.com",
    "http://127.0.0.1/x",
    "http://u:p@127.0.0.1",
    "http://127.0.0.1/?x",
  ])
    assert.throws(() => localURL(url));
});
test("Go signing matches the chain domain and signed amount", () => {
  const w = goWallet("01".repeat(32)),
    to = goWallet("02".repeat(32));
  const tx = goSign(
    w,
    "gochain-checkpoint-amm-test-v1",
    to.address,
    123456789n,
    "bridge:ascii",
    1,
  );
  const key = createPublicKey({
    key: Buffer.concat([
      Buffer.from("302a300506032b6570032100", "hex"),
      Buffer.from(w.public_key, "hex"),
    ]),
    format: "der",
    type: "spki",
  });
  const payload = {
    chain: "gochain-checkpoint-amm-test-v1",
    from: tx.from,
    to: tx.to,
    amount_atoms: tx.amount_atoms,
    memo: tx.memo,
    nonce: tx.nonce,
  };
  assert(
    verify(
      null,
      Buffer.from(JSON.stringify(payload)),
      key,
      Buffer.from(tx.signature, "hex"),
    ),
  );
  assert(
    !verify(
      null,
      Buffer.from(JSON.stringify({ ...payload, chain: "other" })),
      key,
      Buffer.from(tx.signature, "hex"),
    ),
  );
});
test("source confirmation depth, duplicate identity and post-mint reorg halt", () => {
  const tx = goSign(
    goWallet("01".repeat(32)),
    "test",
    goWallet("02".repeat(32)).address,
    1n,
    "bridge",
    1,
  );
  const blocks = Array.from({ length: 4 }, (_, index) => ({
    index,
    hash: String(index),
    prev_hash: String(index - 1),
    txs: index === 1 ? [tx] : [],
  }));
  assert.equal(findTransfer(blocks.slice(0, 3), tx, 3), undefined);
  assert.deepEqual(findTransfer(blocks, tx, 3), { height: 1, hash: "1" });
  assert.equal(findTransfer(blocks, { ...tx, amount_atoms: 2 }, 3), undefined);
  const operations: Operation[] = [
    {
      id: "deposit",
      kind: "wrap",
      anchor: { height: 1, hash: "1" },
      done: true,
    },
  ];
  checkAnchors(operations, blocks);
  assert.throws(
    () =>
      checkAnchors(
        operations,
        blocks.map((b) => (b.index === 1 ? { ...b, hash: "fork" } : b)),
      ),
    /HALTED/,
  );
});
test("backing reserves burned-but-unpaid withdrawals", () => {
  const op: Operation = {
    id: "burn",
    kind: "unwrap",
    atoms: "200",
    solConfirmed: true,
  };
  assert.equal(backing(1000n, 800n, [op]).surplus_atoms, "0");
  assert.throws(() => backing(999n, 800n, [op]), /HALTED/);
  assert.throws(() => backing(799n, 800n, []), /HALTED/);
  assert.equal(
    backing(800n, 800n, [{ ...op, done: true }]).pending_withdrawal_atoms,
    "0",
  );
});
test("pruned history requires a durable prior finalized receipt; contradictions and rollback halt", () => {
  checkFinalizedReceipt(100, 101, 200, null);
  checkFinalizedReceipt(100, 0, 200, {
    slot: 100,
    err: null,
    confirmationStatus: "finalized",
  });
  assert.throws(
    () => checkFinalizedReceipt(undefined, 101, 200, null),
    /HALTED/,
  );
  assert.throws(() => checkFinalizedReceipt(100, 99, 200, null), /HALTED/);
  assert.throws(() => checkFinalizedReceipt(100, 101, 99, null), /rolled back/);
  assert.throws(
    () =>
      checkFinalizedReceipt(100, 101, 200, {
        slot: 100,
        err: { failure: true },
        confirmationStatus: "finalized",
      }),
    /HALTED/,
  );
});
test("durable intent reload, concurrent operator exclusion, torn journal fail closed", async () => {
  const dir = await mkdtemp(join(tmpdir(), "go-bridge-"));
  try {
    await exclusive(dir, async () => {
      await assert.rejects(
        exclusive(dir, async () => {}),
        /busy/,
      );
      const journal = new Journal(dir);
      journal.state.operations.push({
        id: "one",
        kind: "wrap",
        atoms: "1",
        sol: { raw: "signed-bytes", signature: "same-signature", expiry: 50 },
      });
      await journal.save();
    });
    const reloaded = new Journal(dir);
    await reloaded.load();
    assert.equal(reloaded.state.operations[0].sol?.signature, "same-signature");
    assert(
      !(await readFile(join(dir, "journal.jsonl"), "utf8")).includes("seed"),
    );
    await appendFile(join(dir, "journal.jsonl"), '{"partial":');
    await assert.rejects(new Journal(dir).load(), /Torn/);
  } finally {
    await rm(dir, { recursive: true });
  }
});
