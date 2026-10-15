import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm, readFile, appendFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve, sep } from "node:path";
import { claimFunding } from "./faucet-journal";
const identity = "11111111111111111111111111111111";

test("concurrent claims have one winner; interrupted and completed claims survive reopen", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "go-pools-faucet-test-"));
  t.after(async () => {
    assert(resolve(directory).startsWith(resolve(tmpdir()) + sep));
    assert(directory.includes("go-pools-faucet-test-"));
    await rm(directory, { recursive: true });
  });
  const claims = await Promise.allSettled(
    Array.from({ length: 12 }, () =>
      claimFunding(directory, "localnet", identity, identity),
    ),
  );
  const winners = claims.filter(
    (
      r,
    ): r is PromiseFulfilledResult<Awaited<ReturnType<typeof claimFunding>>> =>
      r.status === "fulfilled",
  );
  assert.equal(winners.length, 1);
  assert.equal(claims.filter((r) => r.status === "rejected").length, 11);
  const winner = winners[0].value;
  await winner.record({
    status: "mint-confirmed",
    signature: "test-only-signature",
  });
  await winner.close();
  const path = join(directory, "localnet", identity, identity + ".jsonl");
  assert.equal((await readFile(path, "utf8")).trim().split("\n").length, 2);
  await assert.rejects(
    claimFunding(directory, "localnet", identity, identity),
    /already attempted/,
  );
  const otherAddress = "1".repeat(31) + "2";
  const completed = await claimFunding(
    directory,
    "localnet",
    identity,
    otherAddress,
  );
  await completed.record({
    status: "completed",
    signatures: ["test-only-signature"],
  });
  await completed.close();
  await assert.rejects(
    claimFunding(directory, "localnet", identity, otherAddress),
    /already attempted/,
  );
  // A torn append must never make a processed/ambiguous request eligible again.
  await appendFile(path, '{"status":"comp');
  await assert.rejects(
    claimFunding(directory, "localnet", identity, identity),
    /already attempted/,
  );
});

test("claim identifiers cannot escape the runtime directory", async () => {
  for (const [network, genesis, address] of [
    ["mainnet", identity, identity],
    ["localnet", "../other", identity],
    ["localnet", identity, "../wallet"],
  ])
    await assert.rejects(
      claimFunding("unused", network, genesis, address),
      /Invalid/,
    );
});
