import "./styles.css";
import {
  Keypair,
  PublicKey,
  Transaction,
  VersionedTransaction,
} from "@solana/web3.js";
import { Deployment, Wallet, keyWallet } from "./solana";
import { Market, SwapQuote, DepositQuote, formatUnits } from "./market";
const $ = <T extends HTMLElement = HTMLElement>(id: string) => {
  const e = document.getElementById(id);
  if (!e) throw Error("Missing " + id);
  return e as T;
};
const text = (id: string, value: string) => {
  $(id).textContent = value;
};
let deployment: Deployment;
let market: Market;
let wallet: Wallet | null = null;
let busy = false,
  refreshing = false;
let inputSymbol = "testUSD";
let quote: SwapQuote | null = null;
let depositQuote: DepositQuote | null = null;
let withdrawQuote: Awaited<ReturnType<Market["quoteWithdraw"]>> | null = null;
let currentAction = "";
let activityCount = 0;
let last: Awaited<ReturnType<Market["snapshot"]>> | null = null;
const shortened = (s: string) => s.slice(0, 5) + "…" + s.slice(-5);
function error(e: unknown) {
  $("error").hidden = false;
  text("error", e instanceof Error ? e.message : String(e));
}
function clearError() {
  $("error").hidden = true;
}
function explorer(address: string, kind: "address" | "tx" = "address") {
  return (
    "https://explorer.solana.com/" +
    kind +
    "/" +
    encodeURIComponent(address) +
    (deployment.network === "devnet"
      ? "?cluster=devnet"
      : "?cluster=custom&customUrl=" + encodeURIComponent(deployment.rpc))
  );
}
function view(name: string) {
  for (const id of ["swap", "pools", "liquidity"])
    $("view-" + id).hidden = id !== name;
  document
    .querySelectorAll<HTMLButtonElement>(".nav")
    .forEach((b) => b.classList.toggle("active", b.dataset.view === name));
  history.replaceState(null, "", "#" + name);
}
function invalidate() {
  quote = null;
  $("swap").hidden = true;
  $("getQuote").hidden = false;
  text("swapOutput", "—");
  text("swapMinimum", "—");
  text("swapFee", "—");
  text("swapImpact", "—");
}
function invalidateDeposit() {
  depositQuote = null;
  $("deposit").hidden = true;
  $("quoteDeposit").hidden = false;
  text("depositUSD", "—");
  text("depositMinimum", "—");
}
function invalidateWithdraw() {
  withdrawQuote = null;
  $("withdraw").hidden = true;
  $("quoteWithdraw").hidden = false;
}
function requireWallet() {
  if (!wallet) throw Error("Connect a wallet first.");
}
function activity(label: string, sig: string) {
  activityCount++;
  text("activityCount", String(activityCount));
  if (activityCount === 1) $("activity").replaceChildren();
  const row = document.createElement("div");
  row.className = "activity-item";
  const title = document.createElement("strong");
  title.textContent = label;
  const link = document.createElement("a");
  link.href = explorer(sig, "tx");
  link.target = "_blank";
  link.rel = "noreferrer";
  link.textContent = shortened(sig) + " ↗";
  const status = document.createElement("small");
  status.textContent = "Confirmed";
  row.append(title, link, status);
  $("activity").prepend(row);
}
async function perform(fn: () => Promise<void>) {
  if (busy) return;
  busy = true;
  clearError();
  document.querySelectorAll<HTMLButtonElement>("button").forEach((b) => {
    if (!["approve", "reject"].includes(b.id)) b.disabled = true;
  });
  try {
    await fn();
  } catch (e) {
    error(e);
  } finally {
    busy = false;
    document
      .querySelectorAll<HTMLButtonElement>("button")
      .forEach((b) => (b.disabled = false));
    $("fund").toggleAttribute("disabled", !wallet);
    $("copy").toggleAttribute("disabled", !wallet);
    await refresh();
  }
}
function requestApproval(): Promise<void> {
  return new Promise((resolve, reject) => {
    const dialog = $<HTMLDialogElement>("approval");
    text("approvalSummary", currentAction);
    const finish = (yes: boolean) => {
      dialog.close();
      $("approve").onclick = null;
      $("reject").onclick = null;
      dialog.oncancel = null;
      yes ? resolve() : reject(Error("Transaction cancelled."));
    };
    $("approve").onclick = () => finish(true);
    $("reject").onclick = () => finish(false);
    dialog.oncancel = (e) => {
      e.preventDefault();
      finish(false);
    };
    dialog.showModal();
  });
}
async function setWallet(w: Wallet, name: string) {
  wallet = w;
  market = new Market(deployment, w);
  await market.check();
  text("walletName", name);
  text("walletAddress", shortened(w.publicKey.toBase58()));
  $("walletAddress").title = w.publicKey.toBase58();
  text("connect", shortened(w.publicKey.toBase58()));
  $("disconnect").hidden = false;
  $("copy").removeAttribute("disabled");
  $("fund").removeAttribute("disabled");
  invalidate();
  invalidateDeposit();
  invalidateWithdraw();
  $<HTMLDialogElement>("walletDialog").close();
  await refresh();
}
async function refresh() {
  if (!market || refreshing) return;
  refreshing = true;
  try {
    last = await market.snapshot();
    text("price", String(last.price));
    text("reserveGO", last.reserveGO);
    text("reserveUSD", last.reserveUSD);
    text("liveStatus", "Live");
    text("updated", "Updated " + new Date().toLocaleTimeString());
    if (wallet) {
      text("walletGO", last.go);
      text("walletUSD", last.usd);
      text("walletSOL", last.sol);
      text("lpBalance", last.lp);
      text("lpShare", last.share.toFixed(4) + "%");
      text(
        "lpClaim",
        `Estimated claim: ${last.claimGO} GO + ${last.claimUSD} testUSD. Fees remain in the reserves; the mix changes with trades.`,
      );
      text(
        "inputBalance",
        "Balance: " + (inputSymbol === "GO" ? last.go : last.usd),
      );
      text(
        "outputBalance",
        "Balance: " + (inputSymbol === "GO" ? last.usd : last.go),
      );
    }
  } catch (e) {
    text("liveStatus", "Unavailable");
    error(e);
  } finally {
    refreshing = false;
  }
}
$("connect").onclick = () => {
  $<HTMLDialogElement>("walletDialog").showModal();
};
$("closeWallet").onclick = () => $<HTMLDialogElement>("walletDialog").close();
for (const id of ["phantom", "solflare"])
  $(id).onclick = () =>
    perform(async () => {
      const windowWallet = window as any;
      const provider =
        id === "phantom" ? windowWallet.phantom?.solana : windowWallet.solflare;
      if (!provider)
        throw Error(
          `${id === "phantom" ? "Phantom" : "Solflare"} was not detected. Install the wallet extension, or use a local session wallet.`,
        );
      await provider.connect();
      const w: Wallet = {
        publicKey: new PublicKey(provider.publicKey.toString()),
        signTransaction: (tx) => provider.signTransaction(tx),
        signAllTransactions: (txs) => provider.signAllTransactions(txs),
      };
      await setWallet(w, id === "phantom" ? "Phantom" : "Solflare");
    });
$("session").onclick = () =>
  perform(async () => {
    if (deployment.network !== "localnet")
      throw Error(
        "Session wallets are only enabled for the local validator. Use a devnet-configured wallet for devnet.",
      );
    const key = Keypair.generate();
    const signer = keyWallet(key);
    const w: Wallet = {
      publicKey: key.publicKey,
      async signTransaction<T extends Transaction | VersionedTransaction>(
        tx: T,
      ) {
        await requestApproval();
        return signer.signTransaction(tx);
      },
      async signAllTransactions<T extends Transaction | VersionedTransaction>(
        txs: T[],
      ) {
        await requestApproval();
        return signer.signAllTransactions(txs);
      },
    };
    await setWallet(w, "Local session wallet");
  });
$("disconnect").onclick = () => {
  wallet = null;
  market = new Market(deployment, {
    publicKey: Keypair.generate().publicKey,
    async signTransaction() {
      throw Error("Read only");
    },
    async signAllTransactions() {
      throw Error("Read only");
    },
  });
  text("connect", "Connect wallet ↗");
  text("walletName", "Not connected");
  text("walletAddress", "Choose a wallet to begin");
  for (const id of [
    "walletGO",
    "walletUSD",
    "walletSOL",
    "lpBalance",
    "lpShare",
  ])
    text(id, "—");
  text("lpClaim", "Connect a wallet to see your claim on the pool.");
  $("fund").setAttribute("disabled", "");
  $("copy").setAttribute("disabled", "");
  $("disconnect").hidden = true;
  invalidate();
  invalidateDeposit();
  invalidateWithdraw();
};
$("copy").onclick = async () => {
  if (wallet) {
    await navigator.clipboard.writeText(wallet.publicKey.toBase58());
    text("walletAddress", "Address copied");
  }
};
$("fund").onclick = () =>
  perform(async () => {
    requireWallet();
    text("fund", "Funding test wallet…");
    const r = await fetch("/api/fund", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ address: wallet!.publicKey.toBase58() }),
    });
    const body = await r.json();
    text("fund", "Get test tokens ＋");
    if (!r.ok) throw Error(body.error);
    for (const sig of body.signatures) activity("Test wallet funding", sig);
  });
$("swapAmount").addEventListener("input", invalidate);
$("reverse").onclick = () => {
  inputSymbol = inputSymbol === "GO" ? "testUSD" : "GO";
  text("inputSymbol", inputSymbol);
  text("outputSymbol", inputSymbol === "GO" ? "testUSD" : "GO");
  const inIcon = $("inputIcon"),
    outIcon = $("outputIcon");
  inIcon.className =
    "token small " + (inputSymbol === "GO" ? "go-token" : "usd-token");
  outIcon.className =
    "token small " + (inputSymbol === "GO" ? "usd-token" : "go-token");
  inIcon.textContent = inputSymbol === "GO" ? "g" : "$";
  outIcon.textContent = inputSymbol === "GO" ? "$" : "g";
  $<HTMLInputElement>("swapAmount").value = inputSymbol === "GO" ? "1" : "100";
  invalidate();
  if (last) {
    text(
      "inputBalance",
      "Balance: " + (inputSymbol === "GO" ? last.go : last.usd),
    );
    text(
      "outputBalance",
      "Balance: " + (inputSymbol === "GO" ? last.usd : last.go),
    );
  }
};
$("getQuote").onclick = () =>
  perform(async () => {
    quote = await market.quoteSwap(
      inputSymbol,
      $<HTMLInputElement>("swapAmount").value,
    );
    const out = inputSymbol === "GO" ? "testUSD" : "GO";
    text("swapOutput", formatUnits(quote.output));
    text("swapMinimum", formatUnits(quote.minimum) + " " + out);
    text("swapFee", formatUnits(quote.fee) + " " + inputSymbol);
    text("swapImpact", quote.impact + "%");
    $("getQuote").hidden = true;
    $("swap").hidden = false;
  });
$("swap").onclick = () =>
  perform(async () => {
    requireWallet();
    if (!quote) throw Error("Get a new quote first.");
    const out = quote.symbol === "GO" ? "testUSD" : "GO";
    currentAction = `Swap ${formatUnits(quote.input)} ${quote.symbol}\nReceive at least ${formatUnits(quote.minimum)} ${out}\nPool fee: 0.3% · network: ${deployment.network}`;
    const sig = await market.swap(quote);
    activity(`Swapped ${formatUnits(quote.input)} ${quote.symbol}`, sig);
    invalidate();
  });
$("depositGO").addEventListener("input", invalidateDeposit);
$("withdrawShares").addEventListener("input", invalidateWithdraw);
$("quoteDeposit").onclick = () =>
  perform(async () => {
    depositQuote = await market.quoteDeposit(
      $<HTMLInputElement>("depositGO").value,
    );
    const usd = deployment.symbolA === "GO" ? depositQuote.b : depositQuote.a;
    text("depositUSD", formatUnits(usd));
    text("depositMinimum", formatUnits(depositQuote.minimum));
    $("quoteDeposit").hidden = true;
    $("deposit").hidden = false;
  });
$("deposit").onclick = () =>
  perform(async () => {
    requireWallet();
    if (!depositQuote) throw Error("Preview deposit first.");
    const q = depositQuote;
    currentAction = `Deposit up to ${formatUnits(deployment.symbolA === "GO" ? q.a : q.b)} GO + ${formatUnits(deployment.symbolA === "GO" ? q.b : q.a)} testUSD\nReceive at least ${formatUnits(q.minimum)} LP shares\nNetwork: ${deployment.network}`;
    activity("Added liquidity", await market.deposit(q));
    invalidateDeposit();
  });
$("quoteWithdraw").onclick = () =>
  perform(async () => {
    requireWallet();
    withdrawQuote = await market.quoteWithdraw(
      $<HTMLInputElement>("withdrawShares").value,
    );
    text(
      "withdrawGO",
      formatUnits(
        deployment.symbolA === "GO" ? withdrawQuote.a : withdrawQuote.b,
      ),
    );
    text(
      "withdrawUSD",
      formatUnits(
        deployment.symbolA === "GO" ? withdrawQuote.b : withdrawQuote.a,
      ),
    );
    $("quoteWithdraw").hidden = true;
    $("withdraw").hidden = false;
  });
$("withdraw").onclick = () =>
  perform(async () => {
    requireWallet();
    if (!withdrawQuote) throw Error("Preview withdrawal first.");
    const q = withdrawQuote;
    currentAction = `Redeem ${formatUnits(q.shares)} LP shares\nReceive at least ${formatUnits(deployment.symbolA === "GO" ? q.minA : q.minB)} GO + ${formatUnits(deployment.symbolA === "GO" ? q.minB : q.minA)} testUSD\nNetwork: ${deployment.network}`;
    activity("Withdrew liquidity", await market.withdraw(q));
    invalidateWithdraw();
  });
$("depositTab").onclick = () => {
  $("depositForm").hidden = false;
  $("withdrawForm").hidden = true;
  $("depositTab").classList.add("selected");
  $("withdrawTab").classList.remove("selected");
};
$("withdrawTab").onclick = () => {
  $("depositForm").hidden = true;
  $("withdrawForm").hidden = false;
  $("depositTab").classList.remove("selected");
  $("withdrawTab").classList.add("selected");
};
document
  .querySelectorAll<HTMLButtonElement>(".nav")
  .forEach((b) => (b.onclick = () => view(b.dataset.view!)));
$("showPool").onclick = () => view("pools");
$("goDeposit").onclick = () => view("liquidity");
async function start() {
  try {
    const response = await fetch("/deployment.json", { cache: "no-store" });
    if (!response.ok)
      throw Error("Pool is not configured. Run npm run setup first.");
    deployment = await response.json();
    market = new Market(deployment, {
      publicKey: Keypair.generate().publicKey,
      async signTransaction() {
        throw Error("Connect a wallet");
      },
      async signAllTransactions() {
        throw Error("Connect a wallet");
      },
    });
    await market.check();
    text(
      "network",
      deployment.network === "devnet" ? "Solana devnet" : "Solana localnet",
    );
    text(
      "networkNote",
      deployment.network === "devnet"
        ? "Solana devnet · test tokens only · separate from mined GoChain GO"
        : "Local Solana validator · real program execution with test tokens · devnet funding pending",
    );
    $<HTMLAnchorElement>("programLink").href = explorer(deployment.program);
    $("session").hidden = deployment.network !== "localnet";
    const fields = [
      ["Program", deployment.program],
      ["Pool", deployment.pool],
      ["GO mint", market.mint("GO").toBase58()],
      ["testUSD mint", market.mint("testUSD").toBase58()],
      ["LP mint", deployment.lpMint],
    ];
    for (const [name, address] of fields) {
      const dt = document.createElement("dt");
      dt.textContent = name;
      const dd = document.createElement("dd");
      const a = document.createElement("a");
      a.href = explorer(address);
      a.textContent = address + " ↗";
      a.target = "_blank";
      a.rel = "noreferrer";
      dd.append(a);
      $("addresses").append(dt, dd);
    }
    await refresh();
    setInterval(refresh, 5000);
    view(
      ["swap", "pools", "liquidity"].includes(location.hash.slice(1))
        ? location.hash.slice(1)
        : "swap",
    );
  } catch (e) {
    text("network", "Not configured");
    error(e);
  }
}
void start();
