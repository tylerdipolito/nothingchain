import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { randomBytes } from "node:crypto";
import { resolve } from "node:path";
import { runBridge } from "./bridge";

const args = process.argv.slice(2);
const dirIndex = args.indexOf("--dir"),
  portIndex = args.indexOf("--port");
const directory = resolve(dirIndex >= 0 ? args[dirIndex + 1] : ".local/bridge");
const port = Number(portIndex >= 0 ? args[portIndex + 1] : 5180);
if (!Number.isInteger(port) || port < 1024 || port > 65535)
  throw Error("Invalid bridge UI port");
const host = `127.0.0.1:${port}`,
  token = randomBytes(32).toString("hex");
const html = (
  await readFile(new URL("./bridge-ui.html", import.meta.url), "utf8")
).replaceAll("__TOKEN__", token);
// Verify configuration before exposing even the local operator UI. No auto-init/reset.
await runBridge(directory, "status", []);
let busy = false;
const allowed = new Set([
  "status",
  "quote",
  "mine",
  "wrap",
  "unwrap",
  "recover",
  "seed-pool",
  "swap",
]);
createServer(async (req, res) => {
  res.setHeader("Cache-Control", "no-store");
  res.setHeader("X-Frame-Options", "DENY");
  res.setHeader("X-Content-Type-Options", "nosniff");
  res.setHeader("Referrer-Policy", "no-referrer");
  res.setHeader(
    "Content-Security-Policy",
    `default-src 'none'; script-src 'nonce-${token}'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`,
  );
  let acquired = false;
  try {
    if (req.headers.host !== host)
      throw Error("Use the exact loopback bridge URL");
    if (req.method === "GET" && req.url === "/") {
      res.setHeader("Content-Type", "text/html; charset=utf-8");
      res.end(html);
      return;
    }
    if (
      req.method !== "POST" ||
      req.url !== "/api" ||
      req.headers.origin !== `http://${host}` ||
      req.headers["x-bridge-token"] !== token ||
      !req.headers["content-type"]?.startsWith("application/json")
    )
      throw Error("Invalid local operator request");
    if (busy) {
      res.statusCode = 409;
      throw Error("Bridge operator busy");
    }
    busy = true;
    acquired = true;
    let body = "";
    for await (const part of req) {
      body += part;
      if (body.length > 2048) throw Error("Request too large");
    }
    const data = JSON.parse(body);
    if (
      !allowed.has(data.command) ||
      !Array.isArray(data.args) ||
      data.args.length > 3 ||
      data.args.some((a: unknown) => typeof a !== "string" || a.length > 100)
    )
      throw Error("Invalid bridge command");
    const result = await runBridge(directory, data.command, data.args);
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify(result));
  } catch (e: any) {
    res.statusCode = res.statusCode === 409 ? 409 : 400;
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ error: e.message }));
  } finally {
    if (acquired) busy = false;
  }
}).listen(port, "127.0.0.1", () =>
  console.log(
    `Local trusted bridge UI: http://${host}/ (dedicated server-held test wallets only)`,
  ),
);
