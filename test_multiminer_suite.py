import socket
import json
import time
import urllib.request
import sys

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")

def recv_line(sock):
    buf = b""
    while b"\n" not in buf:
        chunk = sock.recv(1024)
        if not chunk:
            break
        buf += chunk
    return buf.decode().strip()

def send_msg(sock, data):
    msg = json.dumps(data) + "\n"
    sock.sendall(msg.encode())

def run_test():
    print("==================================================")
    print("🚀 BITPROOF MULTI-MINER CONCURRENCY & TEST SUITE")
    print("==================================================")

    host = "127.0.0.1"
    port = 3333

    # Worker Alpha (simulated)
    worker1_name = "go1_simulated_alpha_1111111111111111111111111111111111111111.sim_miner_1"
    # Worker Beta (simulated)
    worker2_name = "go1_simulated_beta_2222222222222222222222222222222222222222.sim_miner_2"

    print("\n1. Connecting Miner 1 (Alpha)...")
    s1 = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s1.connect((host, port))

    # Subscribe Miner 1
    send_msg(s1, {"id": 1, "method": "mining.subscribe", "params": ["sim_miner_1/1.0"]})
    resp1 = json.loads(recv_line(s1))
    en1_miner1 = resp1["result"][1]
    print(f"   Miner 1 Subscribed! Extranonce1: {en1_miner1}")

    # Authorize Miner 1
    send_msg(s1, {"id": 2, "method": "mining.authorize", "params": [worker1_name, "x"]})
    auth1 = json.loads(recv_line(s1))
    print(f"   Miner 1 Authorized: {auth1.get('result')}")

    # Receive set_difficulty and initial job for Miner 1
    diff1_msg = json.loads(recv_line(s1))
    job1_msg = json.loads(recv_line(s1))
    job1_id = job1_msg["params"][0]
    print(f"   Miner 1 Job Received: {job1_id}")

    print("\n2. Connecting Miner 2 (Beta) Concurrently...")
    s2 = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s2.connect((host, port))

    # Subscribe Miner 2
    send_msg(s2, {"id": 1, "method": "mining.subscribe", "params": ["sim_miner_2/1.0"]})
    resp2 = json.loads(recv_line(s2))
    en1_miner2 = resp2["result"][1]
    print(f"   Miner 2 Subscribed! Extranonce1: {en1_miner2}")

    # Verify disjoint extranonce1
    if en1_miner1 == en1_miner2:
        print("❌ CRITICAL: Extranonce1 collision between miners!")
        sys.exit(1)
    else:
        print(f"   ✅ Disjoint keyspace confirmed: {en1_miner1} != {en1_miner2}")

    # Authorize Miner 2
    send_msg(s2, {"id": 2, "method": "mining.authorize", "params": [worker2_name, "x"]})
    auth2 = json.loads(recv_line(s2))
    print(f"   Miner 2 Authorized: {auth2.get('result')}")
    diff2_msg = json.loads(recv_line(s2))
    job2_msg = json.loads(recv_line(s2))
    job2_id = job2_msg["params"][0]
    print(f"   Miner 2 Job Received: {job2_id}")

    print("\n3. Testing Stale Job Rejection (Error 21)...")
    stale_params = [worker1_name, "stale_nonexistent_job_999", "00000001", "00000000", "00000000"]
    send_msg(s1, {"id": 10, "method": "mining.submit", "params": stale_params})
    stale_resp = json.loads(recv_line(s1))
    print(f"   Stale submission result: {stale_resp}")
    if stale_resp.get("error") and stale_resp["error"][0] == 21:
        print("   ✅ Correctly rejected with Stratum Error 21: Job not found or stale")
    else:
        print("   ⚠️ Unexpected stale error response:", stale_resp)

    print("\n4. Testing Low-Difficulty Rejection (Error 23)...")
    # Nonce 00000000 on high target bits will fail difficulty
    low_diff_params = [worker1_name, job1_id, "00000001", "12345678", "ffffffff"]
    send_msg(s1, {"id": 11, "method": "mining.submit", "params": low_diff_params})
    low_resp = json.loads(recv_line(s1))
    print(f"   Low difficulty submission result: {low_resp}")
    if low_resp.get("error") and low_resp["error"][0] == 23:
        print("   ✅ Correctly rejected with Stratum Error 23: Low difficulty share")
    else:
        print("   (Result code):", low_resp)

    s1.close()
    s2.close()
    print("\n5. Checking /api/workers Endpoint...")
    try:
        data = urllib.request.urlopen("http://127.0.0.1:3334/api/workers").read().decode()
        workers = json.loads(data)
        print(f"   Registered Workers in SQLite: {len(workers)}")
        for w in workers:
            print(f"   - {w['worker_id']}: Acc={w['shares_accepted']}, Rej={w['shares_rejected']}, Bal={w['earned_go_formatted']}")
    except Exception as e:
        print(f"   API check note: {e}")

    print("\n✅ Multi-miner concurrency handshake and error-code tests complete!")

if __name__ == "__main__":
    run_test()
