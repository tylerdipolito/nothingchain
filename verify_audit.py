import urllib.request
import json
import subprocess

def main():
    print("=== BITPROOF AUDIT VERIFICATION ===")
    
    # 1. Dashboard API stats
    try:
        data = urllib.request.urlopen("http://127.0.0.1:3334/api/stats").read().decode()
        stats = json.loads(data)
        btc = stats["bitcoin_parent"]
        go = stats["gochain_aux"]
        print(f"[Dashboard API] Status: OK")
        print(f"  Bitcoin Height:       {btc['height']}")
        print(f"  Bitcoin Blocks Won:   {btc['blocks_mined']}")
        print(f"  Bitcoin Difficulty:   {btc['difficulty']:.4e} (Bits: {btc['target_bits']})")
        print(f"  Bitcoin Tip Hash:     {btc['tip_hash']}")
        print(f"  GoChain Height:       {go['height']}")
        print(f"  GoChain Difficulty:   {go['difficulty']:.2f}")
        print(f"  GoChain Miner Bal:    {go['miner_balance_go']}")
        print(f"  Bitaxe Worker:        {stats.get('worker', 'None')}")
        print(f"  Shares Accepted:      {stats.get('accepted_shares', 0)}")
        print(f"  Shares Rejected:      {stats.get('rejected_shares', 0)}")
    except Exception as e:
        print(f"[Dashboard API] Error: {e}")

    # 2. Bitcoin Core blockchain info via RPC or bitcoin-cli
    try:
        cli_path = "/home/wner/bitcoin/bitcoin-28.0/bin/bitcoin-cli"
        cmd = ["wsl", cli_path, "-regtest", "getblockchaininfo"]
        res = subprocess.run(cmd, capture_output=True, text=True)
        raw = res.stdout[res.stdout.find('{'):]
        info = json.loads(raw)
        print(f"\n[Bitcoin Core Live Node]")
        print(f"  Chain:                {info['chain']}")
        print(f"  Blocks:               {info['blocks']}")
        print(f"  BestBlockHash:        {info['bestblockhash']}")
        print(f"  Difficulty:           {info['difficulty']:.4e}")
    except Exception as e:
        print(f"[Bitcoin Core Live Node] Error: {e}")

    # 3. UTXO scan for miner address
    try:
        bash_cmd = f"{cli_path} -regtest scantxoutset start '[\"addr(bcrt1qxl26let6yc2fn8n33he4fswzsyys83h86qdzg6)\"]'"
        res = subprocess.run(["wsl", "bash", "-c", bash_cmd], capture_output=True, text=True)
        raw = res.stdout[res.stdout.find('{'):]
        scan = json.loads(raw)
        print(f"\n[Bitcoin Core UTXO Set]")
        print(f"  UTXO Set Height:      {scan['height']}")
        print(f"  Confirmed UTXOs:      {len(scan.get('unspents', []))}")
        print(f"  Total Amount Mined:   {scan['total_amount']} BTC")
    except Exception as e:
        print(f"[Bitcoin Core UTXO Set] Error: {e} | STDERR: {res.stderr}")

if __name__ == "__main__":
    main()
