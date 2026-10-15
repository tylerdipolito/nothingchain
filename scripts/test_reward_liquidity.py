import unittest
from reward_liquidity import plan, atoms, UNIT

class DepositPlanTests(unittest.TestCase):
    def setUp(self):
        self.pool={'go_reserve_atoms':20*UNIT,'testusd_reserve_atoms':2000*UNIT,'total_lp_shares_atoms':200*UNIT}
    def test_allocation_and_budget(self):
        p=plan(self.pool,4*UNIT,2*UNIT,500*UNIT,100*UNIT,10000*UNIT)
        self.assertEqual(p,{'a':2*UNIT,'b':200*UNIT,'min_a':1980000000})
    def test_insufficient_matching_funds(self):
        self.assertIsNone(plan(self.pool,2*UNIT,2*UNIT,500*UNIT,100*UNIT,199*UNIT))
    def test_no_rewards_no_spend(self):
        self.assertIsNone(plan(self.pool,0,2*UNIT,500*UNIT,100*UNIT,10000*UNIT))
    def test_rounding_and_bounds(self):
        self.pool['testusd_reserve_atoms']+=1
        p=plan(self.pool,2*UNIT,2*UNIT,500*UNIT,100*UNIT,10000*UNIT)
        self.assertEqual(p['b'],200*UNIT+1)
        for n in ['0','-1','0.000000001','NaN','Infinity']:
            with self.assertRaises(ValueError): atoms(n)


class JournalSafetyTests(unittest.TestCase):
    def test_different_journals_cannot_run_concurrently_for_same_wallet(self):
        import tempfile
        from pathlib import Path
        from types import SimpleNamespace
        from unittest.mock import patch
        from reward_liquidity import run, acquire_wallet_lock
        with tempfile.TemporaryDirectory() as tmp, patch('reward_liquidity.tempfile.gettempdir', return_value=tmp):
            held = acquire_wallet_lock('same-wallet')
            try:
                for journal in ['first.json', 'second.json']:
                    args = SimpleNamespace(node='http://127.0.0.1:8082', state=str(Path(tmp)/journal), binary='unused', wallet='unused')
                    response = SimpleNamespace(returncode=0, stdout='{"address":"same-wallet"}', stderr='')
                    with patch('reward_liquidity.subprocess.run', return_value=response), patch('reward_liquidity.urllib.request.urlopen', side_effect=AssertionError('No network before wallet lock')):
                        with self.assertRaises(FileExistsError): run(args)
                    self.assertTrue(held.exists(), 'contender must not remove owner lock')
                    self.assertFalse(Path(args.state+'.lock').exists())
                unrelated = acquire_wallet_lock('different-wallet')
                unrelated.unlink()
            finally:
                held.unlink()
            resumed = acquire_wallet_lock('same-wallet')
            resumed.unlink()
    def test_completed_run_never_submits_again(self):
        self.check_journal('completed', False)
    def test_ambiguous_submission_never_retries(self):
        self.check_journal('prepared', True)
    def check_journal(self, status, raises):
        import tempfile, json
        from pathlib import Path
        from types import SimpleNamespace
        from unittest.mock import patch
        from reward_liquidity import run, NETWORK
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'state.json'
            config={'node':'http://127.0.0.1:8082','address':'test-address','network':NETWORK,'percent':20,'go_budget':2*UNIT,'usd_budget':500*UNIT,'confirmations':2}
            path.write_text(json.dumps({'config':config,'status':status}))
            args=SimpleNamespace(node=config['node'],state=str(path),binary='unused',wallet='unused',percent=20,go_budget='2',usd_budget='500',confirmations=2,duration=1)
            result=SimpleNamespace(returncode=0,stdout='{"address":"test-address"}',stderr='')
            with patch('reward_liquidity.subprocess.run',return_value=result) as cli, patch('reward_liquidity.urllib.request.urlopen',side_effect=AssertionError('Must not access network')):
                if raises:
                    with self.assertRaises(RuntimeError):run(args)
                else:run(args)
                self.assertEqual(cli.call_count,1)
                self.assertEqual(cli.call_args.args[0][1:3],['wallet','address'])
            self.assertFalse(Path(str(path)+'.lock').exists())

if __name__ == "__main__":
    unittest.main()
