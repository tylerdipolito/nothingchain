import test from "node:test";
import assert from "node:assert/strict";
import { parseUnits, formatUnits, outputFor, depositFor } from "./market";
test("amounts retain all six token decimals", () => {
  assert.equal(parseUnits("123.000001"), 123000001n);
  assert.equal(formatUnits(123000001n), "123.000001");
  for (const v of [
    "-1",
    "0",
    "1e3",
    "1.0000001",
    "NaN",
    "18446744073709551616",
  ])
    assert.throws(() => parseUnits(v));
});
test("swap output matches integer formula and preserves product", () => {
  const x = 18097823n,
    y = 1995000000n,
    a = 100000000n;
  const out = outputFor(a, y, x);
  assert.equal(out, 861389n);
  assert((y + a) * (x - out) >= x * y);
  assert.throws(() => outputFor(1n, 1000000000n, 1n));
});
test("deposit uses proportional amounts, rounds safely, and rejects dust", () => {
  assert.deepEqual(
    depositFor(2000000n, 500000000n, 200000000n, 20000000n, 2000000000n),
    { a: 2000000n, b: 200000000n, shares: 20000000n },
  );
  assert.throws(() => depositFor(1n, 1n, 1n, 1000n, 1000n));
});
