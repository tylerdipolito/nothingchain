import { readFile, writeFile, mkdir } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { randomUUID, createHash } from "node:crypto";
import {
  Connection,
  Keypair,
  PublicKey,
  SystemProgram,
  Transaction,
  TransactionInstruction,
  SYSVAR_RENT_PUBKEY,
} from "@solana/web3.js";
import {
  createAssociatedTokenAccountIdempotentInstruction,
  createInitializeMint2Instruction,
  createMintToInstruction,
  createBurnInstruction,
  getAssociatedTokenAddressSync,
  getMint,
  getAccount,
  MINT_SIZE,
  TOKEN_PROGRAM_ID,
} from "@solana/spl-token";
import { utils } from "@coral-xyz/anchor";
import BN from "bn.js";
import {
  assertNetwork,
  context,
  keyWallet,
  poolAddresses,
  PROGRAM,
} from "../src/solana";
import { outputFor } from "../src/market";
import { chartHistory } from "./bridge-chart";
import {
  amount,
  backing,
  Block,
  checkAnchors,
  checkFinalizedReceipt,
  display,
  exclusive,
  findTransfer,
  goSign,
  goWallet,
  GoWallet,
  Journal,
  localURL,
  Operation,
} from "./bridge-core";

interface Config {
  version: 1;
  node: string;
  rpc: string;
  chain: string;
  goGenesis: string;
  solGenesis: string;
  custody: string;
  userGo: string;
  authority: string;
  userSol: string;
  mint: string;
  usdMint: string;
  confirmations: number;
}
interface Keys {
  custody: GoWallet;
  userGo: GoWallet;
  authority: number[];
  userSol: number[];
  mint: number[];
  usdMint: number[];
}
const safeInteger = (v: number) => {
  if (!Number.isSafeInteger(v) || v < 0)
    throw Error("Unsafe GoChain numeric value");
  return BigInt(v);
};

export class Bridge {
  request?: { id: string; hash: string };
  operation(kind: Operation["kind"], atoms?: string): Operation {
    return {
      id: this.request?.id || randomUUID(),
      requestHash: this.request?.hash,
      kind,
      ...(atoms ? { atoms } : {}),
    };
  }
  c!: Connection;
  config!: Config;
  keys!: Keys;
  journal: Journal;
  constructor(readonly directory: string) {
    this.journal = new Journal(directory);
  }
  async go(path: string, body?: unknown): Promise<any> {
    const res = await fetch(this.config.node + path, {
      method: body ? "POST" : "GET",
      headers: { "Content-Type": "application/json" },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(15000),
      redirect: "error",
    });
    if (!res.ok)
      throw Error(`GoChain ${path}: ${res.status} ${await res.text()}`);
    return res.json();
  }
  authority() {
    return Keypair.fromSecretKey(Uint8Array.from(this.keys.authority));
  }
  user() {
    return Keypair.fromSecretKey(Uint8Array.from(this.keys.userSol));
  }
  mint() {
    return new PublicKey(this.config.mint);
  }
  usd() {
    return new PublicKey(this.config.usdMint);
  }
  async load() {
    this.config = JSON.parse(
      await readFile(`${this.directory}/config.json`, "utf8"),
    );
    this.keys = JSON.parse(
      await readFile(`${this.directory}/keys.json`, "utf8"),
    );
    if (
      this.config.version !== 1 ||
      this.config.chain !== "gochain-checkpoint-amm-test-v1" ||
      this.config.confirmations !== 3
    )
      throw Error("Unsupported bridge policy");
    localURL(this.config.node);
    localURL(this.config.rpc);
    for (const name of ["custody", "userGo"] as const) {
      const derived = goWallet(this.keys[name].seed);
      if (
        derived.address !== this.config[name] ||
        derived.public_key !== this.keys[name].public_key ||
        derived.address !== this.keys[name].address
      )
        throw Error("Go key mismatch");
    }
    for (const name of ["authority", "userSol", "mint", "usdMint"] as const)
      if (
        Keypair.fromSecretKey(
          Uint8Array.from(this.keys[name]),
        ).publicKey.toBase58() !== this.config[name]
      )
        throw Error("Solana key mismatch");
    this.c = new Connection(this.config.rpc, {
      commitment: "finalized",
      disableRetryOnRateLimit: true,
      confirmTransactionInitialTimeout: 60000,
    });
    await this.journal.load();
    if (
      !this.journal.state.operations.some(
        (o) => o.id === "setup" && o.kind === "setup",
      )
    )
      throw Error(
        "Bridge journal missing setup history. Manual reconciliation required; never initialize again against this mint.",
      );
    await this.networks();
  }
  async networks() {
    await assertNetwork(this.c, { network: "localnet", rpc: this.config.rpc });
    if ((await this.c.getGenesisHash()) !== this.config.solGenesis)
      throw Error("Solana ledger identity changed");
    const [stats, blocks] = (await Promise.all([
      this.go("/stats"),
      this.go("/chain"),
    ])) as [any, Block[]];
    if (
      stats.chain_id !== this.config.chain ||
      !stats.valid ||
      !stats.checkpoint_required ||
      !stats.checkpoint_ready ||
      blocks[0]?.hash !== this.config.goGenesis
    )
      throw Error(
        "GoChain identity, validation or trusted checkpoint not ready",
      );
    for (let i = 0; i < blocks.length; i++)
      if (
        blocks[i].index !== i ||
        (i && blocks[i].prev_hash !== blocks[i - 1].hash)
      )
        throw Error("Inconsistent GoChain snapshot");
    checkAnchors(this.journal.state.operations, blocks);
    const finalized = this.journal.state.operations.filter(
      (o) => o.solConfirmed,
    );
    const [firstAvailable, finalizedTip] = await Promise.all([
      this.c.getFirstAvailableBlock(),
      this.c.getSlot("finalized"),
    ]);
    for (let i = 0; i < finalized.length; i += 100) {
      const batch = finalized.slice(i, i + 100);
      const statuses = (
        await this.c.getSignatureStatuses(
          batch.map((o) => o.sol!.signature),
          { searchTransactionHistory: true },
        )
      ).value;
      for (let j = 0; j < batch.length; j++) {
        const status = statuses[j],
          observed = batch[j].sol!.finalizedSlot;
        // Trust our fsynced observation of finalized history only after the RPC has
        // explicitly pruned that slot. An unresolved transaction gets no such exception.
        checkFinalizedReceipt(observed, firstAvailable, finalizedTip, status);
      }
    }
    return blocks;
  }
  async init(node: string, rpc: string) {
    await mkdir(this.directory, { recursive: true });
    // Exclusive configuration creation: never reuse a mint with a new/empty journal.
    const custody = goWallet(),
      userGo = goWallet();
    const authority = Keypair.generate(),
      userSol = Keypair.generate(),
      mint = Keypair.generate(),
      usdMint = Keypair.generate();
    this.keys = {
      custody,
      userGo,
      authority: [...authority.secretKey],
      userSol: [...userSol.secretKey],
      mint: [...mint.secretKey],
      usdMint: [...usdMint.secretKey],
    };
    this.config = {
      version: 1,
      node: localURL(node),
      rpc: localURL(rpc),
      chain: "gochain-checkpoint-amm-test-v1",
      goGenesis: "",
      solGenesis: "",
      custody: custody.address,
      userGo: userGo.address,
      authority: authority.publicKey.toBase58(),
      userSol: userSol.publicKey.toBase58(),
      mint: mint.publicKey.toBase58(),
      usdMint: usdMint.publicKey.toBase58(),
      confirmations: 3,
    };
    this.c = new Connection(this.config.rpc, "finalized");
    await assertNetwork(this.c, { network: "localnet", rpc: this.config.rpc });
    this.config.solGenesis = await this.c.getGenesisHash();
    this.config.goGenesis = (await this.go("/chain"))[0].hash;
    await this.networks();
    await writeFile(`${this.directory}/keys.json`, JSON.stringify(this.keys), {
      flag: "wx",
      mode: 0o600,
    });
    await writeFile(
      `${this.directory}/config.json`,
      JSON.stringify(this.config, null, 2),
      { flag: "wx", mode: 0o600 },
    );
    this.journal.state.operations.push({ id: "setup", kind: "setup" });
    await this.journal.save();
    // Faucet only funds transaction fees. Never issues wGO.
    for (const key of [authority, userSol]) {
      const sig = await this.c.requestAirdrop(key.publicKey, 2000000000);
      const result = await this.c.confirmTransaction(sig, "finalized");
      if (result.value.err) throw Error("Local SOL airdrop failed");
    }
    await this.setup();
  }
  async prepareSol(
    op: Operation,
    instructions: TransactionInstruction[],
    signer: Keypair,
    extra: Keypair[] = [],
  ) {
    if (op.sol) throw Error("Operation already has a signed transaction");
    const latest = await this.c.getLatestBlockhash("finalized");
    const tx = new Transaction({
      feePayer: signer.publicKey,
      recentBlockhash: latest.blockhash,
    }).add(...instructions);
    tx.sign(signer, ...extra);
    op.sol = {
      raw: tx.serialize().toString("base64"),
      signature: utils.bytes.bs58.encode(tx.signature!),
      expiry: latest.lastValidBlockHeight,
    };
    await this.journal.save(); // Intent and exact signed bytes durable BEFORE broadcast.
  }
  async confirmSol(op: Operation) {
    if (op.solConfirmed) return;
    if (!op.sol) throw Error("Missing signed Solana intent");
    const { raw, signature, expiry } = op.sol;
    let status = (
      await this.c.getSignatureStatuses([signature], {
        searchTransactionHistory: true,
      })
    ).value[0];
    if (status?.err)
      throw Error(
        `Solana operation failed: ${signature} ${JSON.stringify(status.err)}. Reconcile manually.`,
      );
    if (status?.confirmationStatus !== "finalized") {
      if (!status) {
        if ((await this.c.getBlockHeight("finalized")) > expiry)
          throw Error(
            `Expired/unknown operation ${signature}. Reconcile manually; no new signature created.`,
          );
        const returned = await this.c.sendRawTransaction(
          Buffer.from(raw, "base64"),
          {
            skipPreflight: false,
            maxRetries: 0,
            preflightCommitment: "confirmed",
          },
        );
        if (returned !== signature)
          throw Error("RPC returned a different signature");
      }
      const tx = Transaction.from(Buffer.from(raw, "base64"));
      const result = await this.c.confirmTransaction(
        {
          signature,
          blockhash: tx.recentBlockhash!,
          lastValidBlockHeight: expiry,
        },
        "finalized",
      );
      if (result.value.err)
        throw Error(
          `Solana operation failed: ${signature} ${JSON.stringify(result.value.err)}`,
        );
      status = (
        await this.c.getSignatureStatuses([signature], {
          searchTransactionHistory: true,
        })
      ).value[0];
      if (status?.confirmationStatus !== "finalized" || status.err)
        throw Error("Solana transaction not finalized");
    }
    op.sol.finalizedSlot = status!.slot;
    op.solConfirmed = true;
    await this.journal.save();
  }
  async setup() {
    let op = this.journal.state.operations.find((o) => o.kind === "setup");
    if (op?.done) return;
    if (!op) {
      op = { id: "setup", kind: "setup" };
      this.journal.state.operations.push(op);
    }
    if (!op.sol) {
      const rent = await this.c.getMinimumBalanceForRentExemption(MINT_SIZE);
      const instructions: TransactionInstruction[] = [];
      const mints = [
        Keypair.fromSecretKey(Uint8Array.from(this.keys.mint)),
        Keypair.fromSecretKey(Uint8Array.from(this.keys.usdMint)),
      ];
      for (const mint of mints)
        instructions.push(
          SystemProgram.createAccount({
            fromPubkey: this.authority().publicKey,
            newAccountPubkey: mint.publicKey,
            lamports: rent,
            space: MINT_SIZE,
            programId: TOKEN_PROGRAM_ID,
          }),
          createInitializeMint2Instruction(
            mint.publicKey,
            8,
            this.authority().publicKey,
            null,
          ),
        );
      const usdATA = getAssociatedTokenAddressSync(
        this.usd(),
        this.user().publicKey,
      );
      instructions.push(
        createAssociatedTokenAccountIdempotentInstruction(
          this.authority().publicKey,
          usdATA,
          this.user().publicKey,
          this.usd(),
        ),
        createMintToInstruction(
          this.usd(),
          usdATA,
          this.authority().publicKey,
          1000000000000n,
        ),
      );
      await this.prepareSol(op, instructions, this.authority(), mints);
    }
    await this.confirmSol(op);
    op.done = true;
    await this.journal.save();
  }
  async confirmGo(op: Operation) {
    const blocks = await this.networks();
    const found = findTransfer(blocks, op.go!, this.config.confirmations);
    if (found) {
      op.anchor = found;
      await this.journal.save();
      return true;
    }
    if (!findTransfer(blocks, op.go!, 1)) {
      const snapshot = await this.go("/sync");
      if (!snapshot.pending.some((t: any) => t.signature === op.go!.signature))
        await this.go("/tx", op.go); // Exact same signature/nonce only; never construct a second payment.
    }
    return false;
  }
  async mintPolicy() {
    const mint = await getMint(this.c, this.mint(), "finalized");
    if (
      mint.decimals !== 8 ||
      mint.freezeAuthority ||
      !mint.mintAuthority?.equals(this.authority().publicKey)
    )
      throw Error("wGO mint policy changed");
    return mint;
  }
  async audit() {
    const blocks = await this.networks();
    const mint = await this.mintPolicy();
    const account = await this.go(`/balance/${this.config.custody}`);
    // A release included in the current chain already reduced custody. Its obligation
    // remains in the recovery journal until three confirmations; don't count it twice.
    const included = this.journal.state.operations.filter(
      (o) =>
        o.kind === "unwrap" && !o.done && o.go && findTransfer(blocks, o.go, 1),
    );
    const adjusted = this.journal.state.operations.map((o) =>
      included.includes(o) ? { ...o, done: true } : o,
    );
    return {
      ...backing(safeInteger(account.balance_atoms), mint.supply, adjusted),
      confirming_release_atoms: included
        .reduce((n, o) => n + BigInt(o.atoms!), 0n)
        .toString(),
    };
  }
  async recover() {
    await this.networks();
    for (const op of this.journal.state.operations) {
      if (op.done) continue;
      if (op.kind === "setup") {
        await this.setup();
        continue;
      }
      if (op.kind === "wrap") {
        if (!(await this.confirmGo(op))) return;
        await this.audit();
        if (!op.sol) {
          const audit = await this.audit();
          if (BigInt(audit.surplus_atoms) < BigInt(op.atoms!))
            throw Error(
              "BRIDGE HALTED: insufficient unallocated backing for mint",
            );
          const ata = getAssociatedTokenAddressSync(
            this.mint(),
            this.user().publicKey,
          );
          await this.prepareSol(
            op,
            [
              createAssociatedTokenAccountIdempotentInstruction(
                this.authority().publicKey,
                ata,
                this.user().publicKey,
                this.mint(),
              ),
              createMintToInstruction(
                this.mint(),
                ata,
                this.authority().publicKey,
                BigInt(op.atoms!),
              ),
            ],
            this.authority(),
          );
        }
        await this.confirmSol(op);
      } else if (op.kind === "unwrap") {
        await this.confirmSol(op);
        if (!op.go) {
          await this.audit();
          const account = await this.go(`/balance/${this.config.custody}`);
          op.go = goSign(
            this.keys.custody,
            this.config.chain,
            this.config.userGo,
            BigInt(op.atoms!),
            `bridge-release:${op.id}`,
            account.next_nonce,
          );
          await this.journal.save();
        }
        if (!(await this.confirmGo(op))) return;
      } else {
        await this.confirmSol(op);
      }
      op.done = true;
      await this.journal.save();
    }
    await this.audit();
  }
  async ready() {
    await this.recover();
    if (this.journal.state.operations.some((o) => !o.done))
      throw Error(
        "Operation pending. Mine GoChain confirmations, then run recover. Do not repeat wrap/unwrap.",
      );
  }
  async wrap(value: string) {
    await this.ready();
    const n = amount(value);
    const account = await this.go(`/balance/${this.config.userGo}`);
    if (safeInteger(account.available_balance_atoms) < n)
      throw Error("Mine GO to the dedicated bridge user first");
    const op = this.operation("wrap", n.toString());
    op.go = goSign(
      this.keys.userGo,
      this.config.chain,
      this.config.custody,
      n,
      `bridge-wrap:${op.id}:${this.config.userSol}`,
      account.next_nonce,
    );
    this.journal.state.operations.push(op);
    await this.journal.save();
    await this.recover();
  }
  async unwrap(value: string) {
    await this.ready();
    const n = amount(value);
    const ata = getAssociatedTokenAddressSync(
      this.mint(),
      this.user().publicKey,
    );
    if ((await getAccount(this.c, ata)).amount < n)
      throw Error("Insufficient wGO");
    const op = this.operation("unwrap", n.toString());
    this.journal.state.operations.push(op);
    const memo = new TransactionInstruction({
      programId: new PublicKey("MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr"),
      keys: [
        { pubkey: this.user().publicKey, isSigner: true, isWritable: false },
      ],
      data: Buffer.from(
        `go-bridge-v1:${op.id}:${this.config.chain}:${this.config.userGo}`,
      ),
    });
    await this.prepareSol(
      op,
      [createBurnInstruction(ata, this.mint(), this.user().publicKey, n), memo],
      this.user(),
    );
    await this.recover();
  }
  async pool(seedGO: string, seedUSD: string) {
    await this.ready();
    if (this.journal.state.operations.some((o) => o.kind === "pool"))
      throw Error("Pool already initialized; use its existing reserves");
    const sorted = [this.mint(), this.usd()].sort((a, b) =>
      Buffer.compare(a.toBuffer(), b.toBuffer()),
    );
    const [mintA, mintB] = sorted,
      addresses = poolAddresses(mintA, mintB);
    if (!(await this.c.getAccountInfo(PROGRAM))?.executable)
      throw Error("Our AMM program is not deployed here");
    const n = amount(seedGO),
      usd = amount(seedUSD);
    const user = this.user(),
      { program } = context(this.c, keyWallet(user));
    const userA = getAssociatedTokenAddressSync(mintA, user.publicKey),
      userB = getAssociatedTokenAddressSync(mintB, user.publicKey),
      userLp = getAssociatedTokenAddressSync(addresses.lpMint, user.publicKey);
    const tx = new Transaction();
    tx.add(
      await program.methods
        .initialize()
        .accountsStrict({
          payer: user.publicKey,
          mintA,
          mintB,
          ...addresses,
          tokenProgram: TOKEN_PROGRAM_ID,
          systemProgram: SystemProgram.programId,
          rent: SYSVAR_RENT_PUBKEY,
        })
        .instruction(),
    );
    tx.add(
      createAssociatedTokenAccountIdempotentInstruction(
        user.publicKey,
        userLp,
        user.publicKey,
        addresses.lpMint,
      ),
    );
    tx.add(
      await program.methods
        .deposit(
          new BN((mintA.equals(this.mint()) ? n : usd).toString()),
          new BN((mintA.equals(this.mint()) ? usd : n).toString()),
          new BN(1),
          new BN((await this.c.getSlot()) + 300),
        )
        .accountsStrict({
          user: user.publicKey,
          ...addresses,
          userA,
          userB,
          userLp,
          tokenProgram: TOKEN_PROGRAM_ID,
        })
        .instruction(),
    );
    const op = this.operation("pool");
    this.journal.state.operations.push(op);
    await this.prepareSol(op, tx.instructions, user);
    await this.recover();
  }
  async swap(asset: string, value: string, minimum: string) {
    await this.ready();
    if (!this.journal.state.operations.some((o) => o.kind === "pool" && o.done))
      throw Error("Seed the separate wGO pool first");
    if (!["wGO", "testUSD"].includes(asset))
      throw Error("Choose wGO or testUSD");
    const sorted = [this.mint(), this.usd()].sort((a, b) =>
      Buffer.compare(a.toBuffer(), b.toBuffer()),
    );
    const addresses = poolAddresses(sorted[0], sorted[1]);
    const inputMint = asset === "wGO" ? this.mint() : this.usd(),
      aToB = inputMint.equals(sorted[0]);
    const n = amount(value),
      min = amount(minimum),
      user = this.user();
    const tx = await context(this.c, keyWallet(user))
      .program.methods.swap(
        aToB,
        new BN(n.toString()),
        new BN(min.toString()),
        new BN((await this.c.getSlot()) + 300),
      )
      .accountsStrict({
        user: user.publicKey,
        ...addresses,
        userA: getAssociatedTokenAddressSync(sorted[0], user.publicKey),
        userB: getAssociatedTokenAddressSync(sorted[1], user.publicKey),
        tokenProgram: TOKEN_PROGRAM_ID,
      })
      .transaction();
    const op = this.operation("swap");
    this.journal.state.operations.push(op);
    await this.prepareSol(op, tx.instructions, user);
    await this.recover();
  }
  async quote(asset: string, value: string) {
    await this.audit();
    if (!["wGO", "testUSD"].includes(asset))
      throw Error("Choose wGO or testUSD");
    const sorted = [this.mint(), this.usd()].sort((a, b) =>
      Buffer.compare(a.toBuffer(), b.toBuffer()),
    );
    const addresses = poolAddresses(sorted[0], sorted[1]);
    const a = (await getAccount(this.c, addresses.vaultA)).amount,
      b = (await getAccount(this.c, addresses.vaultB)).amount;
    const inputMint = asset === "wGO" ? this.mint() : this.usd();
    const out = outputFor(
      amount(value),
      inputMint.equals(sorted[0]) ? a : b,
      inputMint.equals(sorted[0]) ? b : a,
    );
    return {
      output: display(out),
      minimum: display((out * 99n) / 100n),
      output_asset: asset === "wGO" ? "testUSD" : "wGO",
      fee_bps: 30,
    };
  }
  async status() {
    const audit = await this.audit();
    const tokenBalance = async (mint: PublicKey) => {
      const address = getAssociatedTokenAddressSync(
        mint,
        this.user().publicKey,
      );
      if (!(await this.c.getAccountInfo(address))) return "0";
      return (await getAccount(this.c, address)).amount.toString();
    };
    const sorted = [this.mint(), this.usd()].sort((a, b) =>
      Buffer.compare(a.toBuffer(), b.toBuffer()),
    );
    const addresses = poolAddresses(sorted[0], sorted[1]);
    let pool = null;
    if (await this.c.getAccountInfo(addresses.pool)) {
      const a = (await getAccount(this.c, addresses.vaultA)).amount,
        b = (await getAccount(this.c, addresses.vaultB)).amount;
      const go = sorted[0].equals(this.mint()) ? a : b,
        usd = sorted[0].equals(this.mint()) ? b : a;
      pool = {
        address: addresses.pool.toBase58(),
        wGO: display(go),
        testUSD: display(usd),
        quote_10_testUSD: display(outputFor(amount("10"), usd, go)),
      };
    }
    return {
      warning:
        "LOCAL TEST ONLY. Trusted operator custody/mint authority. Not a trustless bridge. Existing Demo GO is unrelated.",
      config: this.config,
      backing: audit,
      user: {
        GO: (await this.go(`/balance/${this.config.userGo}`)).balance,
        wGO: display(BigInt(await tokenBalance(this.mint()))),
        testUSD: display(BigInt(await tokenBalance(this.usd()))),
      },
      pool,
      chart: await chartHistory(
        this.c,
        this.directory,
        this.config.solGenesis,
        this.config.mint,
        this.config.usdMint,
      ).catch((e: Error) => ({
        error: e.message,
        notice: "Chart unavailable; no prices were invented.",
      })),
      operations: this.journal.state.operations.map(({ sol, go, ...op }) => ({
        ...op,
        solSignature: sol?.signature,
        goSignature: go?.signature,
      })),
    };
  }
}

export async function runBridge(
  directory: string,
  command: string,
  args: string[],
  requestId?: string,
) {
  return exclusive(directory, async () => {
    const bridge = new Bridge(directory);
    if (command === "init")
      await bridge.init(
        args[0] || "http://127.0.0.1:8086",
        args[1] || "http://127.0.0.1:8897",
      );
    else {
      await bridge.load();
      if (requestId !== undefined) {
        if (
          !/^[a-f0-9-]{36}$/.test(requestId) ||
          !["wrap", "unwrap", "seed-pool", "swap"].includes(command)
        )
          throw Error("Invalid transaction request ID");
        const hash = createHash("sha256")
          .update(JSON.stringify({ command, args }))
          .digest("hex");
        const existing = bridge.journal.state.operations.find(
          (o) => o.id === requestId,
        );
        if (existing) {
          if (existing.requestHash !== hash)
            throw Error(
              "Request ID already belongs to different transaction parameters",
            );
          await bridge.recover();
          return bridge.status();
        }
        bridge.request = { id: requestId, hash };
      }
      if (command === "recover") await bridge.recover();
      else if (command === "wrap") await bridge.wrap(args[0]);
      else if (command === "unwrap") await bridge.unwrap(args[0]);
      else if (command === "seed-pool") await bridge.pool(args[0], args[1]);
      else if (command === "swap") await bridge.swap(args[0], args[1], args[2]);
      else if (command === "quote") return bridge.quote(args[0], args[1]);
      else if (command === "mine") {
        const count = Number(args[0] || "1");
        if (!Number.isInteger(count) || count < 1 || count > 10)
          throw Error("Mine 1–10 blocks per request");
        for (let i = 0; i < count; i++)
          await bridge.go("/mine", { miner: bridge.config.userGo });
        await bridge.recover();
      } else if (command !== "status")
        throw Error(
          "Commands: init, status, mine COUNT, wrap GO, recover, unwrap GO, seed-pool GO USD, swap ASSET INPUT MIN_OUTPUT",
        );
    }
    return bridge.status();
  });
}
if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  const args = process.argv.slice(2),
    index = args.indexOf("--dir");
  let directory = ".local/bridge";
  if (index >= 0) {
    directory = args[index + 1];
    args.splice(index, 2);
  }
  const requestIndex = args.indexOf("--request-id");
  let requestId: string | undefined;
  if (requestIndex >= 0) {
    requestId = args[requestIndex + 1];
    if (!requestId) throw Error("Missing --request-id value");
    args.splice(requestIndex, 2);
  }
  runBridge(resolve(directory), args.shift() || "status", args, requestId)
    .then((result) => console.log(JSON.stringify(result, null, 2)))
    .catch((e) => {
      console.error(e.message);
      process.exitCode = 1;
    });
}
