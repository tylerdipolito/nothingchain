import "./styles.css";
import {
  Keypair,
  PublicKey,
  Transaction,
  VersionedTransaction,
} from "@solana/web3.js";
import { Deployment, Wallet, keyWallet } from "./solana";
import { Market, SwapQuote, DepositQuote, formatUnits } from "./market";
import { encryptLocalWallet, decryptLocalWallet } from "./wallet-backup";
import { TransactionUpdate } from "./submit";
import {
  Receipt,
  parseReceipt,
  receiptPending,
  reconcileReceipt,
} from "./receipt";
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
let localKey: Keypair | null = null;
let localGenesis = "";
let transactionReceipt: Receipt | null = null;
const receiptSlot = () =>
  `go-pools.receipt.v1.${localGenesis}.${wallet!.publicKey.toBase58()}`;
function transactionHold() {
  for (const id of ["swap", "deposit", "withdraw"])
    $<HTMLButtonElement>(id).disabled =
      busy || receiptPending(transactionReceipt);
}
function transactionStatus(state: string, signature?: string) {
  $("transactionStatus").hidden = false;
  const labels: Record<string, string> = {
    preparing: "Preparing transaction…",
    signing: "Awaiting wallet approval…",
    ready: "Signed receipt saved; submitting…",
    pending: "Submitted; waiting for confirmation…",
    confirmed: "Transaction confirmed.",
    failed: "Transaction failed on chain.",
    unknown:
      "Outcome unconfirmed. Check this transaction before another trade.",
    expired:
      "Transaction expired without confirmation on this RPC. Get a fresh quote before retrying.",
    "not-sent": "Transaction was not submitted.",
  };
  text("transactionState", labels[state] || state);
  const link = $<HTMLAnchorElement>("transactionLink");
  link.hidden = !signature;
  if (signature) {
    link.href = explorer(signature, "tx");
    link.textContent = shortened(signature) + " ↗";
  }
  $("checkTransaction").hidden = !receiptPending(transactionReceipt);
  transactionHold();
}
function transactionUpdate(event: TransactionUpdate) {
  if (event.state === "ready") {
    const stored = localStorage.getItem(receiptSlot());
    const existing = stored ? parseReceipt(stored) : null;
    if (receiptPending(existing)) {
      transactionReceipt = existing;
      throw Error(
        "An earlier transaction is unresolved. Use Check transaction before another trade.",
      );
    }
    const receipt = parseReceipt(
      JSON.stringify({
        version: 1,
        signature: event.signature,
        lastValidBlockHeight: event.lastValidBlockHeight,
        label: currentAction.split("\n")[0],
        state: "ready",
      }),
    );
    // Refuse submission if we cannot save a receipt for recovery after reload.
    localStorage.setItem(receiptSlot(), JSON.stringify(receipt));
    transactionReceipt = receipt;
  } else if (
    transactionReceipt &&
    event.signature === transactionReceipt.signature &&
    ["pending", "confirmed", "failed", "unknown", "not-sent"].includes(
      event.state,
    )
  ) {
    transactionReceipt.state = event.state as Receipt["state"];
    try {
      localStorage.setItem(receiptSlot(), JSON.stringify(transactionReceipt));
    } catch {
      /* The pre-broadcast receipt remains sufficient for recovery. */
    }
  }
  if (event.state === "not-sent" && receiptPending(transactionReceipt))
    transactionStatus(transactionReceipt!.state, transactionReceipt!.signature);
  else transactionStatus(event.state, event.signature);
}
let backupMode: "save" | "unlock" | "restore" = "save";
const backupSlot = () => "go-pools.encrypted-wallet.v1." + localGenesis;
function rememberBackup(encrypted: string, address: string): string {
  try {
    const existing = localStorage.getItem(backupSlot());
    if (existing && JSON.parse(existing).address !== address)
      return " Another wallet's browser backup was preserved. Use this wallet's downloaded file to restore it.";
    localStorage.setItem(backupSlot(), encrypted);
    return "";
  } catch {
    return " Browser storage was unavailable; keep and restore the downloaded file.";
  }
}
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
    text("fund", "Get test tokens ＋");
    $("copy").toggleAttribute("disabled", !wallet);
    $("saveWallet").toggleAttribute("disabled", !localKey);
    await refresh();
    transactionHold();
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
async function setWallet(w: Wallet, name: string, key: Keypair | null = null) {
  const nextMarket = new Market(deployment, w);
  await nextMarket.check();
  const savedReceipt = localStorage.getItem(
    `go-pools.receipt.v1.${localGenesis}.${w.publicKey.toBase58()}`,
  );
  const restoredReceipt = savedReceipt ? parseReceipt(savedReceipt) : null;
  wallet = w;
  market = nextMarket;
  localKey = key;
  transactionReceipt = restoredReceipt;
  market.onTransaction = transactionUpdate;
  $("transactionStatus").hidden = !transactionReceipt;
  if (transactionReceipt)
    transactionStatus(transactionReceipt.state, transactionReceipt.signature);
  text("walletName", name);
  text("walletAddress", shortened(w.publicKey.toBase58()));
  $("walletAddress").title = w.publicKey.toBase58();
  text("connect", shortened(w.publicKey.toBase58()));
  $("disconnect").hidden = false;
  $("copy").removeAttribute("disabled");
  $("fund").removeAttribute("disabled");
  $("saveWallet").toggleAttribute("disabled", !localKey);
  text(
    "walletPersistence",
    key
      ? "Local test wallet: save an encrypted backup before closing this tab."
      : "Your extension manages wallet persistence and approvals.",
  );
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
    await connectLocalKey(Keypair.generate());
  });
async function connectLocalKey(key: Keypair) {
  if (deployment.network !== "localnet")
    throw Error("Local wallets are only supported on localnet.");
  const signer = keyWallet(key);
  const w: Wallet = {
    publicKey: key.publicKey,
    async signTransaction<T extends Transaction | VersionedTransaction>(tx: T) {
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
  await setWallet(w, "Local test wallet", key);
}
function openBackup(mode: typeof backupMode) {
  if (deployment.network !== "localnet")
    throw Error("Local wallet backups are localnet-only.");
  backupMode = mode;
  text(
    "backupTitle",
    mode === "save"
      ? "Save local wallet"
      : mode === "unlock"
        ? "Unlock saved wallet"
        : "Restore encrypted backup",
  );
  $("backupConfirmationRow").hidden = mode !== "save";
  $("backupFileRow").hidden = mode !== "restore";
  $("backupRememberRow").hidden = mode === "unlock";
  text("backupSubmit", mode === "save" ? "Encrypt and save" : "Unlock wallet");
  text("backupError", "");
  $<HTMLDialogElement>("walletDialog").close();
  $<HTMLDialogElement>("backupDialog").showModal();
}
$("saveWallet").onclick = () => openBackup("save");
$("unlockWallet").onclick = () => openBackup("unlock");
$("restoreWallet").onclick = () => openBackup("restore");
const clearBackupSecrets = () => {
  for (const id of ["backupPassword", "backupConfirmation", "backupInput"])
    $<HTMLInputElement>(id).value = "";
};
$("closeBackup").onclick = () => $<HTMLDialogElement>("backupDialog").close();
$("backupDialog").addEventListener("close", clearBackupSecrets);
$("backupDialog").addEventListener("cancel", (event) => {
  if (busy) event.preventDefault();
});
$("backupSubmit").onclick = async () => {
  if (busy) return;
  const button = $<HTMLButtonElement>("backupSubmit");
  button.disabled = true;
  $<HTMLButtonElement>("closeBackup").disabled = true;
  busy = true;
  try {
    text("backupError", "");
    const password = $<HTMLInputElement>("backupPassword").value;
    if (backupMode === "save") {
      const key = localKey;
      if (!key) throw Error("Connect a local test wallet first.");
      if (password !== $<HTMLInputElement>("backupConfirmation").value)
        throw Error("Passwords do not match.");
      const encrypted = await encryptLocalWallet(key, password, localGenesis);
      let storageWarning = "";
      if ($<HTMLInputElement>("backupRemember").checked) {
        storageWarning = rememberBackup(encrypted, key.publicKey.toBase58());
      }
      const url = URL.createObjectURL(
        new Blob([encrypted], { type: "application/json" }),
      );
      const link = document.createElement("a");
      link.href = url;
      link.download =
        "go-pools-" + key.publicKey.toBase58() + ".encrypted.json";
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 30000);
      text(
        "walletPersistence",
        "Encrypted backup created. Keep the file and password; check that the download completed. Only ciphertext can be remembered in this browser." +
          storageWarning,
      );
    } else {
      const encrypted =
        backupMode === "unlock"
          ? localStorage.getItem(backupSlot())
          : $<HTMLTextAreaElement>("backupInput").value;
      if (!encrypted)
        throw Error(
          "No encrypted backup found. Choose Restore backup to paste your saved file.",
        );
      const key = await decryptLocalWallet(encrypted, password, localGenesis);
      await connectLocalKey(key);
      let storageWarning = "";
      if (
        backupMode === "restore" &&
        $<HTMLInputElement>("backupRemember").checked
      ) {
        storageWarning = rememberBackup(encrypted, key.publicKey.toBase58());
      }
      text(
        "walletPersistence",
        "Restored local wallet. Keep the encrypted file and password to reconnect after reload." +
          storageWarning,
      );
    }
    await refresh();
    $<HTMLDialogElement>("backupDialog").close();
  } catch (e) {
    text("backupError", e instanceof Error ? e.message : String(e));
  } finally {
    clearBackupSecrets();
    busy = false;
    button.disabled = false;
    $<HTMLButtonElement>("closeBackup").disabled = false;
  }
};
$("disconnect").onclick = () => {
  wallet = null;
  localKey = null;
  transactionReceipt = null;
  $("transactionStatus").hidden = true;
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
  $("saveWallet").setAttribute("disabled", "");
  text(
    "walletPersistence",
    "Disconnected. An encrypted browser backup, if saved, remains available under Connect wallet → Unlock saved wallet.",
  );
  invalidate();
  invalidateDeposit();
  invalidateWithdraw();
};
$("checkTransaction").onclick = () =>
  perform(async () => {
    if (!transactionReceipt) return;
    const wasPending = receiptPending(transactionReceipt);
    transactionReceipt.state = await reconcileReceipt(
      market.connection,
      transactionReceipt,
    );
    try {
      localStorage.setItem(receiptSlot(), JSON.stringify(transactionReceipt));
    } catch {
      /* A future reload can recheck the older pending receipt. */
    }
    transactionStatus(transactionReceipt.state, transactionReceipt.signature);
    if (wasPending && transactionReceipt.state === "confirmed")
      activity(
        "Recovered confirmation: " + transactionReceipt.label,
        transactionReceipt.signature,
      );
    invalidate();
    invalidateDeposit();
    invalidateWithdraw();
  });
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
    localGenesis = await market.connection.getGenesisHash();
    text(
      "network",
      deployment.network === "devnet" ? "Solana devnet" : "Solana localnet",
    );
    text(
      "networkNote",
      deployment.network === "devnet"
        ? "Solana devnet · test tokens only · separate from mined GoChain GO"
        : "Local Solana validator · Demo GO and testUSD only · separate from mined GoChain GO · no bridge",
    );
    $<HTMLAnchorElement>("programLink").href = explorer(deployment.program);
    $("session").hidden = deployment.network !== "localnet";
    $("unlockWallet").hidden = deployment.network !== "localnet";
    $("restoreWallet").hidden = deployment.network !== "localnet";
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
