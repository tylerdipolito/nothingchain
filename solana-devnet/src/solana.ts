import {
  Connection,
  Keypair,
  PublicKey,
  Transaction,
  VersionedTransaction,
} from "@solana/web3.js";
import { AnchorProvider, Program, Idl } from "@coral-xyz/anchor";
import idl from "./idl.json";
export const PROGRAM = new PublicKey(
  "4TdBWFzDUrn4Y5fB9mNJHWpdPP8PNyPnxY7s7FcN7AtK",
);
export const DEVNET_GENESIS = "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG";
export interface Wallet {
  publicKey: PublicKey;
  signTransaction<T extends Transaction | VersionedTransaction>(
    tx: T,
  ): Promise<T>;
  signAllTransactions<T extends Transaction | VersionedTransaction>(
    txs: T[],
  ): Promise<T[]>;
}
export interface Deployment {
  network: "devnet" | "localnet";
  rpc: string;
  program: string;
  pool: string;
  mintA: string;
  mintB: string;
  symbolA: string;
  symbolB: string;
  decimals: 6;
  vaultA: string;
  vaultB: string;
  lpMint: string;
  lockedLP: string;
}
export function keyWallet(key: Keypair): Wallet {
  return {
    publicKey: key.publicKey,
    async signTransaction<T extends Transaction | VersionedTransaction>(tx: T) {
      if (tx instanceof VersionedTransaction) tx.sign([key]);
      else tx.partialSign(key);
      return tx;
    },
    async signAllTransactions<T extends Transaction | VersionedTransaction>(
      txs: T[],
    ) {
      for (const tx of txs) {
        if (tx instanceof VersionedTransaction) tx.sign([key]);
        else tx.partialSign(key);
      }
      return txs;
    },
  };
}
export function context(connection: Connection, wallet: Wallet) {
  const provider = new AnchorProvider(connection, wallet, {
    commitment: "confirmed",
    preflightCommitment: "confirmed",
  });
  return { provider, program: new Program(idl as Idl, provider) };
}
export function poolAddresses(mintA: PublicKey, mintB: PublicKey) {
  const [pool] = PublicKey.findProgramAddressSync(
    [Buffer.from("pool"), mintA.toBuffer(), mintB.toBuffer()],
    PROGRAM,
  );
  const pda = (seed: string) =>
    PublicKey.findProgramAddressSync(
      [Buffer.from(seed), pool.toBuffer()],
      PROGRAM,
    )[0];
  return {
    pool,
    vaultA: pda("vault-a"),
    vaultB: pda("vault-b"),
    lpMint: pda("lp"),
    lockedLp: pda("locked"),
  };
}
export async function assertNetwork(
  c: Connection,
  d: Pick<Deployment, "network" | "rpc">,
) {
  const genesis = await c.getGenesisHash();
  if (d.network === "devnet") {
    if (genesis !== DEVNET_GENESIS) throw Error("RPC is not Solana devnet");
  } else {
    const host = new URL(d.rpc).hostname;
    if (
      !["127.0.0.1", "localhost"].includes(host) ||
      [
        DEVNET_GENESIS,
        "5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d",
        "4uhcVJyU9pJkvQyS88uRDiswHXSCkY3zQawwpjk2NsNY",
      ].includes(genesis)
    )
      throw Error("Localnet requires a local validator");
  }
}
