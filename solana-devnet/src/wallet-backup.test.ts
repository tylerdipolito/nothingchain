import test from "node:test";
import assert from "node:assert/strict";
import { Keypair } from "@solana/web3.js";
import { encryptLocalWallet, decryptLocalWallet } from "./wallet-backup";

test("encrypted local wallet restores the same signer without storing plaintext", async () => {
  const key = Keypair.generate(),
    genesis = Keypair.generate().publicKey.toBase58();
  const password = "local test password only";
  const backup = await encryptLocalWallet(key, password, genesis);
  assert(!backup.includes(Array.from(key.secretKey).join(",")));
  const restored = await decryptLocalWallet(backup, password, genesis);
  assert(
    restored.secretKey.every((value, index) => value === key.secretKey[index]),
    "restored signer must retain its secret after temporary buffers are wiped",
  );
  const second = await encryptLocalWallet(key, password, genesis);
  assert.notEqual(JSON.parse(backup).ciphertext, JSON.parse(second).ciphertext);
  await assert.rejects(
    decryptLocalWallet(backup, "wrong password", genesis),
    /Cannot unlock/,
  );
  await assert.rejects(
    decryptLocalWallet(
      backup,
      password,
      Keypair.generate().publicKey.toBase58(),
    ),
    /different local validator/,
  );
  for (const field of ["ciphertext", "iv", "salt"] as const) {
    const modified = JSON.parse(backup);
    modified[field] =
      (modified[field][0] === "0" ? "1" : "0") + modified[field].slice(1);
    await assert.rejects(
      decryptLocalWallet(JSON.stringify(modified), password, genesis),
    );
  }
  const changedAddress = {
    ...JSON.parse(backup),
    address: Keypair.generate().publicKey.toBase58(),
  };
  await assert.rejects(
    decryptLocalWallet(JSON.stringify(changedAddress), password, genesis),
    /Cannot unlock/,
  );
});

test("backup rejects unsupported formats and unbounded work before deriving keys", async () => {
  const devnet = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG";
  await assert.rejects(
    encryptLocalWallet(Keypair.generate(), "long enough password", devnet),
    /local network/,
  );
  await assert.rejects(
    decryptLocalWallet("{}", "unused", devnet),
    /public clusters/,
  );
  await assert.rejects(
    encryptLocalWallet(Keypair.generate(), "short", "invalid"),
    /12 characters/,
  );
  await assert.rejects(
    decryptLocalWallet("x".repeat(4097), "unused", "unused"),
    /too large/,
  );
  for (const input of ["null", "{}", '{"iterations":999999999}', "[]"]) {
    await assert.rejects(
      decryptLocalWallet(input, "unused", "unused"),
      /Unsupported/,
    );
  }
});
