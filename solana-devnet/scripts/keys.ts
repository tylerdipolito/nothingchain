import { mkdir, readFile, writeFile } from "node:fs/promises";
import { Keypair } from "@solana/web3.js";
await mkdir(".local", { recursive: true });
async function key(path: string) {
  try {
    return Keypair.fromSecretKey(
      Uint8Array.from(JSON.parse(await readFile(path, "utf8"))),
    );
  } catch (e: any) {
    if (e.code !== "ENOENT") throw e;
    const k = Keypair.generate();
    await writeFile(path, JSON.stringify(Array.from(k.secretKey)), {
      flag: "wx",
      mode: 0o600,
    });
    return k;
  }
}
const payer = await key(".local/deployer.json"),
  program = await key(".local/goamm-program.json");
const next = program.publicKey.toBase58();
const idl = JSON.parse(await readFile("src/idl.json", "utf8"));
const previous = idl.address;
if (previous !== next) {
  for (const path of [
    "src/solana.ts",
    "src/idl.json",
    "scripts/generate-idl.ts",
    "Anchor.toml",
    "programs/goamm/src/lib.rs",
  ]) {
    const content = await readFile(path, "utf8");
    await writeFile(path, content.replaceAll(previous, next));
  }
}
console.log("Dedicated test payer:", payer.publicKey.toBase58());
console.log("Our AMM program ID:", next);
console.log("Private key files stay in .local/ and are ignored by Git.");
