"""Bounded wallet-side mining-reward liquidity deposit. Standard library only."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request
from urllib.parse import urlparse
from decimal import Decimal

UNIT = 100_000_000
NETWORK = 'gochain-amm-test-v1'

def atoms(value):
    n = Decimal(value) * UNIT
    if not n.is_finite() or n != n.to_integral_value() or n <= 0 or n > 2**63-1:
        raise ValueError('Amounts must be positive with at most eight decimal places')
    return int(n)

def display(n):
    return f'{n // UNIT}.{n % UNIT:08d}'

def plan(pool, eligible, go_budget, usd_budget, go_balance, usd_balance):
    x, y, total = pool['go_reserve_atoms'], pool['testusd_reserve_atoms'], pool['total_lp_shares_atoms']
    if min(x, y, total) <= 0:
        raise ValueError('Seed the pool manually before enabling reward deposits')
    a = min(eligible, go_budget, go_balance)
    b = (a*y+x-1)//x
    if a <= 0 or b > min(usd_budget, usd_balance):
        return None
    shares = min(a*total//x, b*total//y)
    if shares < 100:
        return None
    return {'a': a, 'b': b, 'min_a': shares*99//100}

def save(path, data):
    temp = path.with_suffix('.tmp')
    with temp.open('w', encoding='utf-8') as f:
        json.dump(data, f, indent=2)
        f.flush()
        os.fsync(f.fileno())
    os.replace(temp, path)

def run(args):
    node = args.node.rstrip('/')
    url = urlparse(node)
    if url.scheme != 'http' or url.hostname not in ('127.0.0.1', 'localhost'):
        raise ValueError('Only local test nodes are supported')
    state_path = Path(args.state).resolve()
    state_path.parent.mkdir(parents=True, exist_ok=True)
    lock = Path(str(state_path)+'.lock')
    fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.close(fd)
    try:
        def api(endpoint):
            with urllib.request.urlopen(node+endpoint, timeout=10) as response:
                return json.load(response)
        def cli(*parts):
            result = subprocess.run([str(Path(args.binary).resolve()), *parts], capture_output=True, text=True, encoding='utf-8', timeout=120)
            if result.returncode:
                raise RuntimeError(result.stderr.strip() or 'CLI failed')
            return json.loads(result.stdout)
        addr = cli('wallet', 'address', '--file', str(Path(args.wallet).resolve()))['address']
        config = {'node': node, 'address': addr, 'network': NETWORK, 'percent': args.percent,
                  'go_budget': atoms(args.go_budget), 'usd_budget': atoms(args.usd_budget), 'confirmations': args.confirmations}
        if state_path.exists():
            state = json.loads(state_path.read_text())
            if state['config'] != config:
                raise ValueError('Saved configuration differs; refusing to change authorization')
            if state['status'] == 'completed':
                print('Already completed; no further spending.', flush=True)
                return
            if state['status'] == 'prepared':
                raise RuntimeError('Prior submission outcome is uncertain. Inspect ledger and journal manually; no automatic retry.')
        else:
            stats = api('/stats')
            if stats['chain_id'] != NETWORK or not stats['valid']:
                raise ValueError('Wrong network or invalid ledger')
            state = {'config': config, 'status': 'watching', 'baseline_height': stats['height'], 'baseline_hash': stats['tip_hash']}
            save(state_path, state)
            print(f"Watching NEW rewards after block {stats['height']}; {args.percent}% allocation; one deposit only.", flush=True)
        deadline = time.monotonic()+args.duration
        while time.monotonic() < deadline:
            stats = api('/stats')
            if stats['chain_id'] != NETWORK or not stats['valid']:
                raise RuntimeError('Network/validation check failed')
            chain = api('/chain')
            base = state['baseline_height']
            if len(chain) <= base or chain[base]['hash'] != state['baseline_hash']:
                raise RuntimeError('Baseline reorganized; manual review required')
            if state['status'] == 'queued':
                matches = [b for b in chain for tx in b['txs'] if tx['signature'] == state['signature']]
                if matches and chain[-1]['index']-matches[0]['index']+1 >= args.confirmations:
                    state.update(status='completed', confirmed_block=matches[0]['index'])
                    save(state_path, state)
                    print(f"Deposit confirmed in block {matches[0]['index']}. Stopped; no further spending.", flush=True)
                    return
            else:
                mature = chain[base+1:len(chain)-args.confirmations+1]
                rewards = sum(b['reward_atoms'] for b in mature if b['miner'] == addr)
                eligible = rewards*args.percent//100
                if eligible > 0:
                    response = api('/amm')
                    if response['network'] != NETWORK:
                        raise RuntimeError('Wrong AMM network')
                    pool = response['state']
                    account = api('/balance/'+addr)
                    if api('/stats')['pending_txs']:
                        time.sleep(2)
                        continue
                    proposal = plan(pool, eligible, config['go_budget'], config['usd_budget'], account['available_balance_atoms'], pool['testusd_atoms'].get(addr, 0))
                    if proposal:
                        # Journal the maximum authorized spend before attempting submission.
                        # An interrupted request is deliberately NOT retried on restart.
                        state.update(status='prepared', proposal=proposal, rewards_observed_atoms=rewards)
                        save(state_path, state)
                        result = cli('amm', 'add', '--node', node, '--wallet', str(Path(args.wallet).resolve()),
                                     '--a', display(proposal['a']), '--b', display(proposal['b']), '--min-a', display(proposal['min_a']))
                        state.update(status='queued', signature=result['queued']['signature'])
                        save(state_path, state)
                        print(f"Queued maximum {display(proposal['a'])} GO + {display(proposal['b'])} testUSD; minimum LP shares {display(proposal['min_a'])}.", flush=True)
            time.sleep(2)
        print('Time limit reached. Journal preserved; no additional submission. A queued deposit may still confirm.', flush=True)
    finally:
        lock.unlink()

if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--node', default='http://127.0.0.1:8082')
    p.add_argument('--binary', default='gochain-amm.exe')
    p.add_argument('--wallet', default='amm-provider.wallet.json')
    p.add_argument('--state', default='reward-liquidity.local.json')
    p.add_argument('--percent', type=int, default=20)
    p.add_argument('--go-budget', default='2')
    p.add_argument('--usd-budget', default='500')
    p.add_argument('--confirmations', type=int, default=2)
    p.add_argument('--duration', type=int, default=600)
    a = p.parse_args()
    if not 1 <= a.percent <= 100 or a.confirmations < 1 or a.duration < 1:
        p.error('Invalid percentage, confirmations, or duration')
    run(a)
