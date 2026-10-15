import test from "node:test";
import assert from "node:assert/strict";
import {
  parseReceipt,
  receiptPending,
  reconcileReceipt,
  Receipt,
} from "./receipt";
const receipt: Receipt = {
  version: 1,
  signature: "1".repeat(64),
  lastValidBlockHeight: 100,
  label: "Test swap",
  state: "unknown",
};
function rpc(statuses: unknown[], height = 101) {
  return {
    async getSignatureStatuses() {
      return { value: [statuses.length > 1 ? statuses.shift() : statuses[0]] };
    },
    async getBlockHeight() {
      return height;
    },
  } as unknown as Parameters<typeof reconcileReceipt>[0];
}
test("uncertain receipts survive serialization and block another trade", () => {
  assert(receiptPending(parseReceipt(JSON.stringify(receipt))));
  assert(!receiptPending({ ...receipt, state: "confirmed" }));
  for (const value of [
    "null",
    "{}",
    JSON.stringify({ ...receipt, lastValidBlockHeight: -1 }),
    "x".repeat(2049),
  ])
    assert.throws(() => parseReceipt(value));
});
test("recovery distinguishes confirmed, failed, processed and expired signatures", async () => {
  assert.equal(
    await reconcileReceipt(
      rpc([{ confirmationStatus: "confirmed", err: null }]),
      receipt,
    ),
    "confirmed",
  );
  assert.equal(
    await reconcileReceipt(
      rpc([
        {
          confirmationStatus: "finalized",
          err: { InstructionError: [0, "test"] },
        },
      ]),
      receipt,
    ),
    "failed",
  );
  assert.equal(
    await reconcileReceipt(
      rpc([{ confirmationStatus: "processed", err: null }]),
      receipt,
    ),
    "pending",
  );
  assert.equal(await reconcileReceipt(rpc([null], 99), receipt), "pending");
  assert.equal(await reconcileReceipt(rpc([null, null]), receipt), "expired");
  assert.equal(
    await reconcileReceipt(
      rpc([null, { confirmationStatus: "confirmed", err: null }]),
      receipt,
    ),
    "confirmed",
  );
});
