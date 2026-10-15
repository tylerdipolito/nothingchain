import { Keypair } from "@solana/web3.js";

// Local test wallets only. Encryption protects a saved file, not a compromised
// browser while the wallet is unlocked. No secret is sent to an HTTP service.
const ITERATIONS = 600_000;
const encoder = new TextEncoder();
const publicClusters = new Set([
  "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
  "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d",
  "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY",
]);
type Backup = {
  format: "go-pools-local-wallet";
  version: 1;
  network: "localnet";
  genesis: string;
  address: string;
  iterations: number;
  salt: string;
  iv: string;
  ciphertext: string;
};
const hex = (bytes: Uint8Array) =>
  Array.from(bytes, (n) => n.toString(16).padStart(2, "0")).join("");
function bytes(value: unknown, length: number): Uint8Array<ArrayBuffer> {
  if (
    typeof value !== "string" ||
    value.length !== length * 2 ||
    !/^[0-9a-f]+$/.test(value)
  )
    throw Error("Invalid wallet backup encoding.");
  return Uint8Array.from(value.match(/../g)!, (n) => parseInt(n, 16));
}
function metadata(b: Backup) {
  return encoder.encode(
    JSON.stringify([
      b.format,
      b.version,
      b.network,
      b.genesis,
      b.address,
      b.iterations,
    ]),
  );
}
async function encryptionKey(password: string, salt: Uint8Array<ArrayBuffer>) {
  const material = await crypto.subtle.importKey(
    "raw",
    encoder.encode(password),
    "PBKDF2",
    false,
    ["deriveKey"],
  );
  return crypto.subtle.deriveKey(
    { name: "PBKDF2", salt, iterations: ITERATIONS, hash: "SHA-256" },
    material,
    { name: "AES-GCM", length: 256 },
    false,
    ["encrypt", "decrypt"],
  );
}
export async function encryptLocalWallet(
  key: Keypair,
  password: string,
  genesis: string,
): Promise<string> {
  if (password.length < 12)
    throw Error("Use a backup password of at least 12 characters.");
  if (
    !/^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(genesis) ||
    publicClusters.has(genesis)
  )
    throw Error("Invalid local network identity.");
  const salt = crypto.getRandomValues(new Uint8Array(16));
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const backup: Backup = {
    format: "go-pools-local-wallet",
    version: 1,
    network: "localnet",
    genesis,
    address: key.publicKey.toBase58(),
    iterations: ITERATIONS,
    salt: hex(salt),
    iv: hex(iv),
    ciphertext: "",
  };
  const secret = Uint8Array.from(key.secretKey);
  try {
    const encrypted = await crypto.subtle.encrypt(
      { name: "AES-GCM", iv, additionalData: metadata(backup) },
      await encryptionKey(password, salt),
      secret,
    );
    backup.ciphertext = hex(new Uint8Array(encrypted));
    return JSON.stringify(backup, null, 2);
  } finally {
    secret.fill(0);
  }
}
export async function decryptLocalWallet(
  input: string,
  password: string,
  genesis: string,
): Promise<Keypair> {
  if (publicClusters.has(genesis))
    throw Error("Local wallet backups cannot be used on public clusters.");
  if (input.length > 4096) throw Error("Wallet backup is too large.");
  const b = JSON.parse(input) as Backup;
  if (
    !b ||
    b.format !== "go-pools-local-wallet" ||
    b.version !== 1 ||
    b.network !== "localnet" ||
    b.iterations !== ITERATIONS ||
    typeof b.address !== "string" ||
    !/^[1-9A-HJ-NP-Za-km-z]{32,44}$/.test(b.address)
  )
    throw Error("Unsupported wallet backup.");
  if (b.genesis !== genesis)
    throw Error(
      "Backup belongs to a different local validator. Restore that ledger first.",
    );
  const salt = bytes(b.salt, 16),
    iv = bytes(b.iv, 12),
    ciphertext = bytes(b.ciphertext, 80);
  let secret: Uint8Array | undefined;
  try {
    secret = new Uint8Array(
      await crypto.subtle.decrypt(
        { name: "AES-GCM", iv, additionalData: metadata(b) },
        await encryptionKey(password, salt),
        ciphertext,
      ),
    );
    // web3.js retains this array; give the signer its own copy before wiping.
    const key = Keypair.fromSecretKey(Uint8Array.from(secret));
    if (key.publicKey.toBase58() !== b.address) throw Error("Address mismatch");
    return key;
  } catch {
    throw Error("Cannot unlock wallet: incorrect password or damaged backup.");
  } finally {
    secret?.fill(0);
  }
}
