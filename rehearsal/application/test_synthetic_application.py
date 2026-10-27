"""Real backend acceptance with invented archives; execution requires opt-in."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


RUNTIME = load('synthetic_application_runtime', HERE / 'runner.py')
SCENARIOS = load('synthetic_application_scenarios', HERE / 'scenarios.py')


def golden_consensus():
    reports = []
    for chain in SCENARIOS.CHAINS:
        validators = []
        for address in SCENARIOS.ADDRESSES:
            measured = address in {'g1active', 'g1beta'}
            lag = (0 if chain == SCENARIOS.CHAINS[0] else 900) if address == 'g1active' else 20
            window = {'p50_ms': lag if measured else None, 'p90_ms': lag if measured else None,
                      'sample_count': 100 if measured else 0,
                      'last_sample_at': '2026-01-01T00:00:00Z' if measured else None}
            validators.append({'addr': address, 'precommit_lag': {'last_24h': window}})
        available = chain == SCENARIOS.CHAINS[0]
        reports.append({'chain_id': chain, 'cache_ttl_seconds': 30, 'validators': validators,
                        'bft': {'available': available, 'margin_percent': -100/6 if available else None,
                                'online_voting_power': 2 if available else None,
                                'total_voting_power': 4 if available else None, 'reason': '' if available else 'stale'}})
    return reports


class AcceptanceOracleBoundaries(unittest.TestCase):
    def test_latency_null_zero_unit_and_chain_mutations_are_rejected(self):
        a, b = golden_consensus()
        SCENARIOS.assert_consensus_contract(a, b)
        mutations = [
            lambda x, y: x['validators'][0]['precommit_lag']['last_24h'].update(p50_ms=None),
            lambda x, y: x['validators'][2]['precommit_lag']['last_24h'].update(p50_ms=0),
            lambda x, y: y['validators'][0]['precommit_lag']['last_24h'].update(p50_ms=.9),
            lambda x, y: y.update(chain_id=SCENARIOS.CHAINS[0]),
        ]
        for mutate in mutations:
            left, right = copy.deepcopy(a), copy.deepcopy(b)
            mutate(left, right)
            with self.assertRaises(SCENARIOS.ScenarioFailure):
                SCENARIOS.assert_consensus_contract(left, right)

    def test_bft_zero_and_available_mutations_are_rejected(self):
        a, b = golden_consensus()
        for which, patch in [(0, {'margin_percent': 0}), (1, {'margin_percent': 0}), (1, {'available': True})]:
            pair = copy.deepcopy([a, b])
            pair[which]['bft'].update(patch)
            with self.assertRaises(SCENARIOS.ScenarioFailure):
                SCENARIOS.assert_consensus_contract(*pair)


class SyntheticApplicationBase(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        provided = os.environ.get('SYNTHETIC_APPLICATION_PRIVATE_OUTPUT')
        if provided:
            parent = Path(provided).resolve(strict=True)
            info = parent.stat()
            if info.st_uid != os.getuid() or info.st_mode & 0o777 != 0o700:
                raise RuntimeError('private_output_parent_required')
            cls.root = parent / cls.__name__
            cls.root.mkdir(mode=0o700)
        else:
            temporary = tempfile.TemporaryDirectory(prefix='gnomon-application-')
            cls.addClassCleanup(temporary.cleanup)
            cls.root = Path(temporary.name).resolve()
            cls.root.chmod(0o700)

    def runner(self, name):
        output = self.root / name
        output.mkdir(mode=0o700)
        return RUNTIME.ApplicationRunner(output), output

    def assert_cleaned(self, runner, output, completed):
        self.assertEqual((output / 'application-summary.json').exists(), completed)
        self.assertEqual((output / 'restore' / 'restore-summary.json').exists(), completed)
        journal = output / 'application-journal.private.jsonl'
        events = [json.loads(line) for line in journal.read_text().splitlines()]
        self.assertTrue(any(item['event'] == 'cleanup_verified' for item in events))
        for kind, arguments in [('ps', ['--all', '--quiet']), ('volume', ['ls', '--quiet'])]:
            result = runner.command(runner.restore.docker + [kind, *arguments, '--filter',
                                    'label=com.docker.compose.project=' + runner.project])[0]
            self.assertFalse(result.strip(), 'owned application resources remain')
        if completed:
            report = json.loads((output / 'application-summary.json').read_text())
            self.assertTrue(report['owned_resources_cleanup_verified'])
            self.assertTrue(report['synthetic_only'])
            self.assertFalse(report['production_data_used'])
            self.assertFalse(report['cross_version_compatibility_verified'])
            self.assertFalse(report['production_rollback_verified'])


@unittest.skipUnless(os.environ.get('RUN_SYNTHETIC_APPLICATION_TESTS') == '1', 'synthetic application is opt-in')
class SyntheticApplicationTests(SyntheticApplicationBase):
    def test_real_workers_api_telegram_cache_backfill_and_cleanup(self):
        runner, output = self.runner('application-contracts')
        with runner.open_application() as app:
            checks = SCENARIOS.exercise_application(app)
            rollback = load('synthetic_application_return', HERE / 'test_synthetic_rollback.py')
            checks.update(rollback.exercise_local_return(app))
            app.report['scenario_checks'] = checks
        self.assert_cleaned(runner, output, True)
        self.assertTrue(runner.report['scenario_checks']['api_contract_verified'])


if __name__ == '__main__':
    unittest.main()
