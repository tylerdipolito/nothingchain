import BN from "bn.js";
import { Connection, PublicKey } from "@solana/web3.js";
import {
  createAssociatedTokenAccountIdempotentInstruction,
  getAssociatedTokenAddressSync,
  getMint,
  TOKEN_PROGRAM_ID,
} from "@solana/spl-token";
import { context, Deployment, PROGRAM, assertNetwork, Wallet } from "./solana";
export const UNIT = 1000000n;
export function parseUnits(value: string): bigint {
  if (!/^(0|[1-9]\d*)(\.\d{1,6})?$/.test(value))
    throw Error("Use a positive amount with up to six decimal places.");
  const [whole, fraction = ""] = value.split(".");
  const n = BigInt(whole) * UNIT + BigInt(fraction.padEnd(6, "0"));
  if (n <= 0n || n > 18446744073709551615n)
    throw Error("Amount is outside the token limits.");
  return n;
}
export function formatUnits(value: bigint | string): string {
  const n = BigInt(value.toString());
  const fraction = (n % UNIT).toString().padStart(6, "0").replace(/0+$/, "");
  return `${n / UNIT}${fraction ? "." + fraction : ""}`;
}
export function outputFor(input: bigint, rIn: bigint, rOut: bigint) {
  if (input <= 0n || rIn <= 0n || rOut <= 0n)
    throw Error("Pool needs liquidity");
  const effective = input * 997n;
  const numerator = effective * rOut;
  if (numerator > (1n << 128n) - 1n)
    throw Error("Trade exceeds supported arithmetic bounds");
  const out = numerator / (rIn * 1000n + effective);
  if (out <= 0n || out >= rOut) throw Error("Amount is too small");
  return out;
}
export function depositFor(
  a: bigint,
  b: bigint,
  supply: bigint,
  rA: bigint,
  rB: bigint,
) {
  if (supply <= 0n || rA <= 0n || rB <= 0n)
    throw Error("Pool must be seeded first");
  const shares = [(a * supply) / rA, (b * supply) / rB].reduce((x, y) =>
    x < y ? x : y,
  );
  if (shares <= 0n) throw Error("Deposit is too small");
  return {
    a: (shares * rA + supply - 1n) / supply,
    b: (shares * rB + supply - 1n) / supply,
    shares,
  };
}
export interface SwapQuote {
  symbol: string;
  input: bigint;
  output: bigint;
  minimum: bigint;
  fee: bigint;
  impact: string;
  slot: number;
}
export interface DepositQuote {
  a: bigint;
  b: bigint;
  shares: bigint;
  minimum: bigint;
  slot: number;
}
export class Market {
  connection: Connection;
  deployment: Deployment;
  wallet: Wallet;
  program: ReturnType<typeof context>["program"];
  constructor(d: Deployment, w: Wallet) {
    this.deployment = d;
    this.wallet = w;
    this.connection = new Connection(d.rpc, {
      commitment: "confirmed",
      disableRetryOnRateLimit: true,
    });
    this.program = context(this.connection, w).program;
  }
  async check() {
    await assertNetwork(this.connection, this.deployment);
    if (this.deployment.program !== PROGRAM.toBase58())
      throw Error("Unexpected pool program");
    const p = await (this.program.account as any).pool.fetch(
      new PublicKey(this.deployment.pool),
    );
    for (const [field, expected] of Object.entries({
      mintA: this.deployment.mintA,
      mintB: this.deployment.mintB,
      vaultA: this.deployment.vaultA,
      vaultB: this.deployment.vaultB,
      lpMint: this.deployment.lpMint,
    })) {
      if (p[field].toBase58() !== expected)
        throw Error("Pool configuration mismatch");
    }
  }
  mint(symbol: string) {
    if (!["GO", "testUSD"].includes(symbol)) throw Error("Unknown token");
    return new PublicKey(
      this.deployment.symbolA === symbol
        ? this.deployment.mintA
        : this.deployment.mintB,
    );
  }
  async balance(mint: PublicKey) {
    const ata = getAssociatedTokenAddressSync(mint, this.wallet.publicKey);
    if (!(await this.connection.getAccountInfo(ata))) return 0n;
    return BigInt(
      (await this.connection.getTokenAccountBalance(ata)).value.amount,
    );
  }
  async reserves() {
    const d = this.deployment;
    const [a, b, lp] = await Promise.all([
      this.connection.getTokenAccountBalance(new PublicKey(d.vaultA)),
      this.connection.getTokenAccountBalance(new PublicKey(d.vaultB)),
      getMint(this.connection, new PublicKey(d.lpMint)),
    ]);
    return {
      a: BigInt(a.value.amount),
      b: BigInt(b.value.amount),
      supply: lp.supply,
    };
  }
  async snapshot() {
    const r = await this.reserves();
    const [go, usd, lp, sol] = await Promise.all([
      this.balance(this.mint("GO")),
      this.balance(this.mint("testUSD")),
      this.balance(new PublicKey(this.deployment.lpMint)),
      this.connection.getBalance(this.wallet.publicKey),
    ]);
    const reserveGO = this.deployment.symbolA === "GO" ? r.a : r.b,
      reserveUSD = this.deployment.symbolA === "GO" ? r.b : r.a;
    return {
      price: reserveGO ? Number((reserveUSD * 10000n) / reserveGO) / 10000 : 0,
      reserveGO: formatUnits(reserveGO),
      reserveUSD: formatUnits(reserveUSD),
      feePercent: "0.3",
      go: formatUnits(go),
      usd: formatUnits(usd),
      sol: (sol / 1e9).toFixed(4),
      lp: formatUnits(lp),
      share: r.supply ? Number((lp * 1000000n) / r.supply) / 10000 : 0,
      claimGO: r.supply ? formatUnits((reserveGO * lp) / r.supply) : "0",
      claimUSD: r.supply ? formatUnits((reserveUSD * lp) / r.supply) : "0",
    };
  }
  async quoteSwap(symbol: string, amount: string): Promise<SwapQuote> {
    const input = parseUnits(amount);
    const r = await this.reserves();
    const aToB = this.deployment.symbolA === symbol;
    const rIn = aToB ? r.a : r.b,
      rOut = aToB ? r.b : r.a;
    const output = outputFor(input, rIn, rOut),
      spot = (input * rOut) / rIn;
    return {
      symbol,
      input,
      output,
      minimum: (output * 995n) / 1000n || 1n,
      fee: (input * 3n) / 1000n,
      impact: spot
        ? (Number(((spot - output) * 1000000n) / spot) / 10000).toFixed(2)
        : "0",
      slot: await this.connection.getSlot("confirmed"),
    };
  }
  accounts() {
    const d = this.deployment;
    return {
      user: this.wallet.publicKey,
      pool: new PublicKey(d.pool),
      vaultA: new PublicKey(d.vaultA),
      vaultB: new PublicKey(d.vaultB),
      userA: getAssociatedTokenAddressSync(
        new PublicKey(d.mintA),
        this.wallet.publicKey,
      ),
      userB: getAssociatedTokenAddressSync(
        new PublicKey(d.mintB),
        this.wallet.publicKey,
      ),
      tokenProgram: TOKEN_PROGRAM_ID,
    };
  }
  liquidityAccounts() {
    const d = this.deployment;
    return {
      ...this.accounts(),
      lpMint: new PublicKey(d.lpMint),
      lockedLp: new PublicKey(d.lockedLP),
      userLp: getAssociatedTokenAddressSync(
        new PublicKey(d.lpMint),
        this.wallet.publicKey,
      ),
    };
  }
  atas(includeLP = false) {
    const d = this.deployment;
    return [d.mintA, d.mintB, ...(includeLP ? [d.lpMint] : [])].map((m) => {
      const mint = new PublicKey(m);
      return createAssociatedTokenAccountIdempotentInstruction(
        this.wallet.publicKey,
        getAssociatedTokenAddressSync(mint, this.wallet.publicKey),
        this.wallet.publicKey,
        mint,
      );
    });
  }
  async swap(q: SwapQuote) {
    if ((await this.connection.getSlot("confirmed")) > q.slot + 150)
      throw Error("Quote expired. Get a new quote.");
    return this.program.methods
      .swap(
        this.deployment.symbolA === q.symbol,
        new BN(q.input.toString()),
        new BN(q.minimum.toString()),
        new BN(q.slot + 150),
      )
      .accountsStrict(this.accounts())
      .preInstructions(this.atas())
      .rpc();
  }
  async quoteDeposit(amount: string): Promise<DepositQuote> {
    const input = parseUnits(amount);
    const r = await this.reserves();
    const goReserve = this.deployment.symbolA === "GO" ? r.a : r.b,
      usdReserve = this.deployment.symbolA === "GO" ? r.b : r.a;
    const usd = (input * usdReserve + goReserve - 1n) / goReserve;
    const planned = depositFor(
      this.deployment.symbolA === "GO" ? input : usd,
      this.deployment.symbolA === "GO" ? usd : input,
      r.supply,
      r.a,
      r.b,
    );
    return {
      ...planned,
      minimum: (planned.shares * 995n) / 1000n || 1n,
      slot: await this.connection.getSlot("confirmed"),
    };
  }
  async deposit(q: DepositQuote) {
    return this.program.methods
      .deposit(
        new BN(q.a.toString()),
        new BN(q.b.toString()),
        new BN(q.minimum.toString()),
        new BN(q.slot + 150),
      )
      .accountsStrict(this.liquidityAccounts())
      .preInstructions(this.atas(true))
      .rpc();
  }
  async quoteWithdraw(shares: string) {
    const n = parseUnits(shares),
      r = await this.reserves();
    const a = (n * r.a) / r.supply,
      b = (n * r.b) / r.supply;
    if (a === 0n || b === 0n) throw Error("Withdrawal is too small");
    return {
      shares: n,
      a,
      b,
      minA: (a * 995n) / 1000n || 1n,
      minB: (b * 995n) / 1000n || 1n,
      slot: await this.connection.getSlot("confirmed"),
    };
  }
  async withdraw(q: Awaited<ReturnType<Market["quoteWithdraw"]>>) {
    return this.program.methods
      .withdraw(
        new BN(q.shares.toString()),
        new BN(q.minA.toString()),
        new BN(q.minB.toString()),
        new BN(q.slot + 150),
      )
      .accountsStrict(this.liquidityAccounts())
      .preInstructions(this.atas(true))
      .rpc();
  }
}
