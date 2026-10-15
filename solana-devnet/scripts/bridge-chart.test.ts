import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { Keypair } from "@solana/web3.js";
import { utils } from "@coral-xyz/anchor";
import { PROGRAM, poolAddresses } from "../src/solana";
import { candles, decodeTrade, chartHistory, Trade } from "./bridge-chart";
function fixture(pool = "pool", va = "va", vb = "vb", go = "go", usd = "usd") {
  const data = Buffer.alloc(33);
  createHash("sha256").update("global:swap").digest().copy(data, 0, 0, 8);
  data[8] = 0;
  data.writeBigUInt64LE(1000000000n, 9);
  data.writeBigUInt64LE(9000000n, 17);
  const balances = (a: string, b: string) => [
    { accountIndex: 2, mint: go, uiTokenAmount: { amount: a, decimals: 8 } },
    { accountIndex: 3, mint: usd, uiTokenAmount: { amount: b, decimals: 8 } },
  ];
  return {
    slot: 10,
    blockTime: 100,
    transaction: {
      message: {
        accountKeys: ["user", pool, va, vb, PROGRAM.toBase58()],
        instructions: [
          {
            programIdIndex: 4,
            accounts: [0, 1, 2, 3],
            data: utils.bytes.bs58.encode(data),
          },
        ],
      },
    },
    meta: {
      err: null,
      preTokenBalances: balances("500000000", "50000000000"),
      postTokenBalances: balances("490224916", "51000000000"),
    },
  };
}
test("chart uses finalized swap vault changes, rejects failures, deposits and mixed donations", () => {
  const tx = fixture();
  const decode = (t: any) =>
    decodeTrade(t, "sig", "pool", "va", "vb", "go", "go", "usd", 0);
  assert.deepEqual(decode(tx), {
    signature: "sig",
    slot: 10,
    order: 0,
    time: 100,
    side: "buy",
    goAtoms: "9775084",
    usdAtoms: "1000000000",
  });
  const failed = structuredClone(tx);
  failed.meta.err = { failed: true } as any;
  assert.equal(decode(failed), null);
  const donation = structuredClone(tx);
  donation.meta.postTokenBalances[1].uiTokenAmount.amount = "51100000000";
  assert.equal(decode(donation), null);
  const noTime = structuredClone(tx);
  noTime.blockTime = null as any;
  assert.equal(decode(noTime), null);
  const deposit = structuredClone(tx);
  deposit.transaction.message.instructions[0].data = utils.bytes.bs58.encode(
    Buffer.alloc(33),
  );
  assert.equal(decode(deposit), null);
  const multiple = structuredClone(tx);
  multiple.transaction.message.instructions.push(
    multiple.transaction.message.instructions[0],
  );
  assert.equal(decode(multiple), null);
});
test("OHLC uses chain ordering and integer ratios; gaps have no synthetic candles", () => {
  const trade = (slot: number, usd: number, time = 100): Trade => ({
    signature: String(slot),
    slot,
    order: 0,
    time,
    side: "buy",
    goAtoms: "100000000",
    usdAtoms: String(usd * 100000000),
  });
  const result = candles(
    [
      trade(4, 105),
      trade(1, 100),
      trade(3, 90),
      trade(2, 110),
      trade(5, 120, 181),
    ],
    60,
  );
  assert.equal(result.length, 2);
  assert.deepEqual(
    result.map((c) => c.time),
    [60, 180],
  );
  assert.deepEqual(
    [result[0].open, result[0].high, result[0].low, result[0].close],
    [
      "100.000000000000",
      "110.000000000000",
      "90.000000000000",
      "105.000000000000",
    ],
  );
  assert.equal(result[0].usdVolumeAtoms, "40500000000");
  assert.equal(result[0].goVolumeAtoms, "400000000");
  assert.equal(candles([], 60).length, 0);
  assert.throws(() => candles([], 7));
});
test("captured trades survive RPC pruning and restart without duplicates", async () => {
  const dir = await mkdtemp(join(tmpdir(), "bridge-chart-"));
  try {
    const mints = [
      Keypair.generate().publicKey,
      Keypair.generate().publicKey,
    ].sort((a, b) => Buffer.compare(a.toBuffer(), b.toBuffer()));
    const a = poolAddresses(mints[0], mints[1]);
    let calls = 0;
    const connection = {
      getSignaturesForAddress: async () => [
        { signature: "sig", slot: 10, err: null },
      ],
      getTransaction: async () => {
        calls++;
        return fixture(
          a.pool.toBase58(),
          a.vaultA.toBase58(),
          a.vaultB.toBase58(),
          mints[0].toBase58(),
          mints[1].toBase58(),
        );
      },
      getBlockSignatures: async () => ({ signatures: ["sig"] }),
    } as any;
    const run = (genesis = "local") =>
      chartHistory(
        connection,
        dir,
        genesis,
        mints[0].toBase58(),
        mints[1].toBase58(),
      );
    assert.equal((await run()).tradeCount, 1);
    assert.equal((await run()).tradeCount, 1);
    assert.equal(calls, 1);
    connection.getSignaturesForAddress = async () => [];
    assert.equal((await run()).tradeCount, 1);
    await assert.rejects(run("other-ledger"), /network mismatch/);
  } finally {
    await rm(dir, { recursive: true });
  }
});
