import { createHash } from "node:crypto";
import { writeFile } from "node:fs/promises";
const disc = (s: string) => [
  ...createHash("sha256").update(s).digest().subarray(0, 8),
];
const ac = (name: string, writable = false, signer = false) => ({
  name,
  writable,
  signer,
});
const liquidity = [
  ac("user", false, true),
  ac("pool"),
  ac("vault_a", true),
  ac("vault_b", true),
  ac("lp_mint", true),
  ac("locked_lp", true),
  ac("user_a", true),
  ac("user_b", true),
  ac("user_lp", true),
  ac("token_program"),
];
const arg = (name: string, type: string) => ({ name, type });
const ix = (name: string, accounts: any[], args: any[]) => ({
  name,
  discriminator: disc("global:" + name),
  accounts,
  args,
});
const idl = {
  address: "4TdBWFzDUrn4Y5fB9mNJHWpdPP8PNyPnxY7s7FcN7AtK",
  metadata: { name: "goamm", version: "0.1.0", spec: "0.1.0" },
  instructions: [
    ix(
      "initialize",
      [
        ac("payer", true, true),
        ac("mint_a"),
        ac("mint_b"),
        ac("pool", true),
        ac("vault_a", true),
        ac("vault_b", true),
        ac("lp_mint", true),
        ac("locked_lp", true),
        ac("token_program"),
        ac("system_program"),
        ac("rent"),
      ],
      [],
    ),
    ix("deposit", liquidity, [
      arg("max_a", "u64"),
      arg("max_b", "u64"),
      arg("min_shares", "u64"),
      arg("expires_at_slot", "u64"),
    ]),
    ix("withdraw", liquidity, [
      arg("shares", "u64"),
      arg("min_a", "u64"),
      arg("min_b", "u64"),
      arg("expires_at_slot", "u64"),
    ]),
    ix(
      "swap",
      [
        ac("user", false, true),
        ac("pool"),
        ac("vault_a", true),
        ac("vault_b", true),
        ac("user_a", true),
        ac("user_b", true),
        ac("token_program"),
      ],
      [
        arg("a_to_b", "bool"),
        arg("amount", "u64"),
        arg("min_output", "u64"),
        arg("expires_at_slot", "u64"),
      ],
    ),
  ],
  accounts: [{ name: "Pool", discriminator: disc("account:Pool") }],
  types: [
    {
      name: "Pool",
      type: {
        kind: "struct",
        fields: [
          ...["mint_a", "mint_b", "vault_a", "vault_b", "lp_mint"].map((n) =>
            arg(n, "pubkey"),
          ),
          arg("bump", "u8"),
        ],
      },
    },
  ],
  errors: [
    "MintOrder",
    "ZeroAmount",
    "Slippage",
    "Overflow",
    "EmptyPool",
    "Expired",
  ].map((name, i) => ({ name, code: 6000 + i, msg: name })),
};
await writeFile("src/idl.json", JSON.stringify(idl, null, 2));
console.log("Wrote client interface from program instruction/account layout");
