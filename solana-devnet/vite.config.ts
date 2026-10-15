import { defineConfig } from "vite";
import { readFile, writeFile } from "node:fs/promises";
import { Connection, Keypair, PublicKey } from "@solana/web3.js";
import { getOrCreateAssociatedTokenAccount, mintTo } from "@solana/spl-token";
import { assertNetwork, Deployment } from "./src/solana";
export default defineConfig({
  define: { "process.env": {}, global: "globalThis" },
  server: {
    host: "127.0.0.1",
    port: 5174,
    strictPort: true,
    headers: {
      "X-Frame-Options": "DENY",
      "Referrer-Policy": "no-referrer",
      "X-Content-Type-Options": "nosniff",
    },
    fs: {
      strict: true,
      deny: [
        ".env",
        ".env.*",
        "*.{crt,pem}",
        "**/.git/**",
        "**/.local/**",
        "**/target/**",
        "**/scripts/**",
        "**/*wallet*.json",
        "**/*keypair*.json",
      ],
    },
  },
  plugins: [
    {
      name: "local-test-faucet",
      configureServer(server) {
        let busy = false;
        const requested = new Set<string>();
        server.middlewares.use("/api/fund", async (req, res) => {
          let acquired = false;
          res.setHeader("Content-Type", "application/json");
          res.setHeader("Cache-Control", "no-store");
          try {
            if (
              req.method !== "POST" ||
              req.headers.origin !== "http://127.0.0.1:5174" ||
              req.headers.host !== "127.0.0.1:5174"
            )
              throw Error("Use the local app to request test tokens.");
            if (busy) throw Error("Faucet is busy. Try again.");
            let body = "";
            for await (const part of req) {
              body += part;
              if (body.length > 200) throw Error("Request too large");
            }
            const address = new PublicKey(JSON.parse(body).address);
            if (requested.has(address.toBase58()))
              throw Error(
                "This wallet already received test tokens during this server session.",
              );
            busy = true;
            acquired = true;
            const d: Deployment = JSON.parse(
              await readFile("public/deployment.json", "utf8"),
            );
            const c = new Connection(d.rpc, {
              commitment: "confirmed",
              disableRetryOnRateLimit: true,
            });
            await assertNetwork(c, d);
            const payer = Keypair.fromSecretKey(
              Uint8Array.from(
                JSON.parse(await readFile(".local/deployer.json", "utf8")),
              ),
            );
            // Mark before sending: interrupted requests are never automatically retried.
            const journalPath = `.local/${d.network}-faucet.json`;
            let entries: Record<string, unknown> = {};
            try {
              entries = JSON.parse(await readFile(journalPath, "utf8"));
            } catch (e: any) {
              if (e.code !== "ENOENT") throw e;
            }
            if (entries[address.toBase58()])
              throw Error(
                "Faucet already attempted for this wallet. Inspect journal before retrying.",
              );
            entries[address.toBase58()] = {
              status: "pending",
              time: new Date().toISOString(),
            };
            await writeFile(journalPath, JSON.stringify(entries, null, 2), {
              mode: 0o600,
            });
            requested.add(address.toBase58());
            const signatures: string[] = [];
            if ((await c.getBalance(address)) < 10000000) {
              if (d.network === "localnet") {
                const sig = await c.requestAirdrop(address, 1000000000);
                await c.confirmTransaction(sig, "confirmed");
                signatures.push(sig);
              } else {
                throw Error(
                  "Fund this wallet with devnet SOL at faucet.solana.com first. No real SOL is required.",
                );
              }
            }
            for (const mint of [d.mintA, d.mintB]) {
              const ata = await getOrCreateAssociatedTokenAccount(
                c,
                payer,
                new PublicKey(mint),
                address,
              );
              const amount =
                (mint === d.mintA ? d.symbolA : d.symbolB) === "GO"
                  ? 200000000n
                  : 20000000000n;
              signatures.push(
                await mintTo(
                  c,
                  payer,
                  new PublicKey(mint),
                  ata.address,
                  payer,
                  amount,
                ),
              );
            }
            entries[address.toBase58()] = { status: "completed", signatures };
            await writeFile(journalPath, JSON.stringify(entries, null, 2), {
              mode: 0o600,
            });
            res.end(
              JSON.stringify({
                signatures,
                message: "Received 200 GO and 20,000 testUSD test tokens.",
              }),
            );
          } catch (e: any) {
            res.statusCode = 400;
            res.end(JSON.stringify({ error: e.message }));
          } finally {
            if (acquired) busy = false;
          }
        });
      },
    },
  ],
  build: { target: "es2022" },
});
