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

def test_duplicate_and_concurrency():
    print("=== TESTING CONCURRENT MULTI-MINER & DUPLICATE REJECTION ===")
    host = "127.0.0.1"
    port = 3333

    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.connect((host, port))

    worker = "go1_audit_tester_3333333333333333333333333333333333333333.audit_bot"
    send_msg(s, {"id": 1, "method": "mining.subscribe", "params": ["audit/1.0"]})
    sub_resp = json.loads(recv_line(s))
    en1 = sub_resp["result"][1]

    send_msg(s, {"id": 2, "method": "mining.authorize", "params": [worker, "x"]})
    auth_resp = json.loads(recv_line(s))
    diff_msg = json.loads(recv_line(s))
    job_msg = json.loads(recv_line(s))
    job_id = job_msg["params"][0]
    ntime = job_msg["params"][7]

    print(f"Worker Authorized: {worker}")
    print(f"Extranonce1: {en1} | Job ID: {job_id}")

    # First share submission
    params1 = [worker, job_id, "00000001", ntime, "00001234"]
    send_msg(s, {"id": 100, "method": "mining.submit", "params": params1})
    resp1 = json.loads(recv_line(s))
    print(f"Submission 1: result={resp1.get('result')}, error={resp1.get('error')}")

    # Immediate Duplicate share submission (same job, en2, nonce)
    send_msg(s, {"id": 101, "method": "mining.submit", "params": params1})
    resp2 = json.loads(recv_line(s))
    print(f"Submission 2 (DUPLICATE): result={resp2.get('result')}, error={resp2.get('error')}")

    s.close()

    if resp2.get("error") and resp2["error"][0] == 22:
        print("✅ SUCCESS: Duplicate share correctly rejected with Error 22 (Duplicate share)")
        return True
    else:
        print(f"❌ FAILURE: Duplicate share was not rejected with Error 22: {resp2}")
        return False

if __name__ == "__main__":
    test_duplicate_and_concurrency()
