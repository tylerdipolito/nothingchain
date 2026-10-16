#!/usr/bin/env python3
"""
demo_grc20_multinode.py - Live Multi-Node Demonstration of GoChain GRC-20 Native Token Standard v0.1

Demonstrates:
1. Spawning two independent GoChain nodes on localhost:8191 and localhost:8192
2. Creating GRC-20 token 'Coffee Bucks' (BREW) with initial supply, 6 decimals
3. Performing transfer, mint, burn, and mint-authority revocation
4. Synchronizing state over HTTP network RPC to Node 2
5. Verifying 100% deterministic state root and balance consistency across nodes
"""

import os
import sys
import time
import json
import subprocess
import urllib.request
import urllib.error

if sys.stdout.encoding != 'utf-8':
    try:
        sys.stdout.reconfigure(encoding='utf-8', errors='replace')
    except Exception:
        pass

NODE1_PORT = 8191
NODE2_PORT = 8192
NODE1_URL = f"http://127.0.0.1:{NODE1_PORT}"
NODE2_URL = f"http://127.0.0.1:{NODE2_PORT}"

LEDGER1 = "ledger_node1_grc20.json"
LEDGER2 = "ledger_node2_grc20.json"
WALLET_FILE = ".demo-creator-wallet.json"

def cleanup():
    for f in [LEDGER1, LEDGER1 + ".lock", LEDGER2, LEDGER2 + ".lock", WALLET_FILE]:
        if os.path.exists(f):
            try:
                os.remove(f)
            except Exception:
                pass

def http_req(url, path, method="GET", data=None):
    full_url = url + path
    headers = {"Content-Type": "application/json"}
    body = json.dumps(data).encode("utf-8") if data is not None else None
    req = urllib.request.Request(full_url, data=body, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))

def wait_for_node(url, timeout=10):
    start = time.time()
    while time.time() - start < timeout:
        try:
            res = http_req(url, "/health")
            if res.get("status") == "ok":
                return True
        except Exception:
            time.sleep(0.1)
    raise RuntimeError(f"Node at {url} failed to start within {timeout}s")

def main():
    print("=" * 80)
    print("💎 GOCHAIN GRC-20 NATIVE TOKEN STANDARD v0.1 - MULTI-NODE DEMONSTRATION 💎")
    print("=" * 80)

    cleanup()

    # Step 1: Create a test wallet for creator
    print("\n[1/7] Creating Creator Wallet...")
    cmd = ["go", "run", ".", "wallet", "create", "--file", WALLET_FILE]
    res = subprocess.run(cmd, capture_output=True, text=True, check=True)
    wallet_info = json.loads(res.stdout)
    creator_addr = wallet_info["address"]
    print(f"  Creator Address: {creator_addr}")

    # Load wallet seed
    with open(WALLET_FILE, "r") as f:
        wallet_data = json.load(f)
    creator_seed = wallet_data["seed"]

    # Step 2: Start Node 1
    print(f"\n[2/7] Starting Node 1 on port {NODE1_PORT}...")
    p1 = subprocess.Popen(["go", "run", ".", "serve", "--port", str(NODE1_PORT), "--data", LEDGER1],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        wait_for_node(NODE1_URL)
        print("  Node 1 live and healthy.")

        # Mine 2 blocks on Node 1 to fund creator with 20 GO (to pay 1 GO creation fee)
        print("  Mining 2 blocks to fund creator with 20 GO...")
        http_req(NODE1_URL, "/mine", "POST", {"miner": creator_addr})
        http_req(NODE1_URL, "/mine", "POST", {"miner": creator_addr})
        acct = http_req(NODE1_URL, f"/balance/{creator_addr}")
        print(f"  Creator GO Balance: {acct['balance']} GO")

        # Step 3: Instruction 1 - create_token (Coffee Bucks / BREW)
        print("\n[3/7] Executing Instruction 1: create_token ('Coffee Bucks', BREW, 6 decimals)...")
        create_payload = {
            "private_key": creator_seed,
            "name": "Coffee Bucks",
            "symbol": "BREW",
            "decimals": 6,
            "initial_supply": 1000000,
            "max_supply": 5000000,
            "mint_authority": creator_addr
        }
        create_res = http_req(NODE1_URL, "/token/create", "POST", create_payload)
        token_id = create_res["token_id"]
        print(f"  Created Token ID: {token_id}")
        http_req(NODE1_URL, "/mine", "POST", {"miner": creator_addr})
        print("  Mined block to confirm token creation.")

        # Step 4: Instructions 2, 3, 4, 5
        alice_addr = "go1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        bob_addr = "go1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

        # Instruction 2: transfer_token (250,000 base units to Alice)
        print(f"\n[4/7] Executing Instruction 2: transfer_token (250,000 BREW base units to Alice)...")
        xfer_payload = {
            "private_key": creator_seed,
            "token_id": token_id,
            "recipient": alice_addr,
            "amount": 250000
        }
        http_req(NODE1_URL, "/token/transfer", "POST", xfer_payload)
        http_req(NODE1_URL, "/mine", "POST", {"miner": creator_addr})

        # Instruction 3: mint (500,000 base units to Bob)
        print(f"  Executing Instruction 3: mint (500,000 BREW base units to Bob)...")
        mint_payload = {
            "private_key": creator_seed,
            "token_id": token_id,
            "recipient": bob_addr,
            "amount": 500000
        }
        http_req(NODE1_URL, "/token/mint", "POST", mint_payload)
        http_req(NODE1_URL, "/mine", "POST", {"miner": creator_addr})

        # Instruction 4: burn (Creator burns 50,000 base units)
        print(f"  Executing Instruction 4: burn (Creator burns 50,000 BREW base units)...")
        burn_payload = {
            "private_key": creator_seed,
            "token_id": token_id,
            "amount": 50000
        }
        http_req(NODE1_URL, "/token/burn", "POST", burn_payload)
        http_req(NODE1_URL, "/mine", "POST", {"miner": creator_addr})

        # Instruction 5: set_authority (Permanently revoke mint authority)
        print(f"  Executing Instruction 5: set_authority (Revoke mint authority permanently)...")
        revoke_payload = {
            "private_key": creator_seed,
            "token_id": token_id,
            "authority_type": "mint",
            "new_authority": ""
        }
        http_req(NODE1_URL, "/token/set-authority", "POST", revoke_payload)
        http_req(NODE1_URL, "/mine", "POST", {"miner": creator_addr})

        # Step 5: Query Node 1 state
        print("\n[5/7] Querying Node 1 State...")
        root1 = http_req(NODE1_URL, "/token/state-root")
        meta1 = http_req(NODE1_URL, f"/token/metadata/{token_id}")
        bal_creator1 = http_req(NODE1_URL, f"/token/balance/{token_id}/{creator_addr}")
        bal_alice1 = http_req(NODE1_URL, f"/token/balance/{token_id}/{alice_addr}")
        bal_bob1 = http_req(NODE1_URL, f"/token/balance/{token_id}/{bob_addr}")

        print(f"  Node 1 Height:     {root1['height']}")
        print(f"  Node 1 State Root: {root1['state_root']}")
        print(f"  Token Supply:      {meta1['total_supply']} base units")
        print(f"  Mint Authority:    '{meta1['mint_authority']}' (REVOKED)")
        print(f"  Creator Balance:   {bal_creator1['formatted']} BREW ({bal_creator1['balance']} atoms)")
        print(f"  Alice Balance:     {bal_alice1['formatted']} BREW ({bal_alice1['balance']} atoms)")
        print(f"  Bob Balance:       {bal_bob1['formatted']} BREW ({bal_bob1['balance']} atoms)")

        # Step 6: Start Node 2 and sync chain history from Node 1
        print(f"\n[6/7] Starting Node 2 on port {NODE2_PORT} & Synchronizing Chain...")
        p2 = subprocess.Popen(["go", "run", ".", "serve", "--port", str(NODE2_PORT), "--data", LEDGER2],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            wait_for_node(NODE2_URL)
            print("  Node 2 live. Fetching snapshot from Node 1...")
            snap = http_req(NODE1_URL, "/sync")
            # Adopt all blocks on Node 2 sequentially
            for b in snap["blocks"][1:]:
                http_req(NODE2_URL, "/block", "POST", b)
            print("  Node 2 synchronized all blocks.")

            # Step 7: Verify Multi-Node Deterministic Consistency
            print("\n[7/7] Verifying Strict Multi-Node Consistency...")
            root2 = http_req(NODE2_URL, "/token/state-root")
            meta2 = http_req(NODE2_URL, f"/token/metadata/{token_id}")
            bal_creator2 = http_req(NODE2_URL, f"/token/balance/{token_id}/{creator_addr}")
            bal_alice2 = http_req(NODE2_URL, f"/token/balance/{token_id}/{alice_addr}")
            bal_bob2 = http_req(NODE2_URL, f"/token/balance/{token_id}/{bob_addr}")

            print("\n" + "=" * 80)
            print("CONSENSUS VERIFICATION COMPARISON")
            print("=" * 80)
            print(f"{'Metric':<25} | {'Node 1 (Port 8191)':<24} | {'Node 2 (Port 8192)':<24}")
            print("-" * 80)
            print(f"{'Chain Height':<25} | {root1['height']:<24} | {root2['height']:<24}")
            print(f"{'Token State Root':<25} | {root1['state_root'][:18]}... | {root2['state_root'][:18]}...")
            print(f"{'Total Supply':<25} | {meta1['total_supply']:<24} | {meta2['total_supply']:<24}")
            print(f"{'Mint Authority':<25} | {meta1['mint_authority'] or 'REVOKED':<24} | {meta2['mint_authority'] or 'REVOKED':<24}")
            print(f"{'Creator Balance':<25} | {bal_creator1['balance']:<24} | {bal_creator2['balance']:<24}")
            print(f"{'Alice Balance':<25} | {bal_alice1['balance']:<24} | {bal_alice2['balance']:<24}")
            print(f"{'Bob Balance':<25} | {bal_bob1['balance']:<24} | {bal_bob2['balance']:<24}")
            print("=" * 80)

            assert root1["state_root"] == root2["state_root"], "State roots do not match!"
            assert meta1["total_supply"] == meta2["total_supply"] == 1450000, "Supply mismatch!"
            assert bal_creator1["balance"] == bal_creator2["balance"] == 700000, "Creator balance mismatch!"
            assert bal_alice1["balance"] == bal_alice2["balance"] == 250000, "Alice balance mismatch!"
            assert bal_bob1["balance"] == bal_bob2["balance"] == 500000, "Bob balance mismatch!"
            assert meta2["mint_authority"] == "", "Mint authority must be revoked on Node 2!"

            print("🎉 ALL CONSENSUS ASSERTIONS PASSED! 100% DETERMINISTIC STATE ROOT SYNC VERIFIED!")
            print("=" * 80)

        finally:
            p2.terminate()
            p2.wait()

    finally:
        p1.terminate()
        p1.wait()
        cleanup()

if __name__ == "__main__":
    main()
