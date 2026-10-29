"""Owned local image selection and restart, never production rollback proof."""
import json
import os
import unittest

from test_synthetic_application import RUNTIME, SCENARIOS, SyntheticApplicationBase


def exercise_local_return(app):
    before = app.sql("SELECT md5(string_agg(row_to_json(p)::text,'' ORDER BY id)) FROM public.archive_probe p").strip()
    observations = []
    def raw_state():
        chains = ','.join(SCENARIOS.quoted(chain) for chain in SCENARIOS.CHAINS)
        return json.loads(app.sql("SELECT json_build_object('rows',count(*),'payload_md5',"
                                  "md5(string_agg(row_to_json(p)::text,'' ORDER BY id))) "
                                  "FROM public.daily_participations p WHERE chain_id IN (" + chains + ")").strip())
    for role, arguments in [('candidate', []), ('baseline', ['./server'])]:
        raw_before = raw_state()
        app.restart_backend(image=role, arguments=arguments)
        SCENARIOS.require(set(app.request('/info')['enabled_chains']) == set(SCENARIOS.CHAINS), 'restart_api_smoke')
        app.wait_for(lambda: all(f'[valoper][{chain}] moniker sync:' in app.backend_log_text()
                                 for chain in SCENARIOS.CHAINS), timeout=90)
        raw_after = raw_state()
        SCENARIOS.require(raw_after == raw_before, 'restart_unexplained_participation_mutation')
        for chain in SCENARIOS.CHAINS:
            active = app.sql("SELECT count(*) FROM public.addr_monikers WHERE chain_id=" +
                             SCENARIOS.quoted(chain) + " AND addr IN ('g1active','g1beta','g1gamma','g1delta') AND voting_power<>0").strip()
            SCENARIOS.require(active == '0', 'restart_departed_voting_power_not_reset')
        observations.append({'role': role, 'raw_rows_before': raw_before['rows'],
                             'raw_rows_after': raw_after['rows'], 'raw_payload_unchanged': True,
                             'departed_fixture_voting_power_zeroed': True})
        # Startup refreshes the mock valset and may clean ghost report rows.
        # Reintroduce the explicit report fixture for HTTP contract smokes;
        # this is not a claim that application writes were rolled back.
        SCENARIOS.seed_report_contract(app, SCENARIOS.utc_reference())
        SCENARIOS.assert_consensus_contract(
            app.request('/api/reports/consensus?chain=' + SCENARIOS.CHAINS[0]),
            app.request('/api/reports/consensus?chain=' + SCENARIOS.CHAINS[1]))
        SCENARIOS.assert_health_contract(app.request('/api/reports/validators?chain=' + SCENARIOS.CHAINS[0]))
        SCENARIOS.require(app.mock_request('/state')['unexpected'] == 0, 'restart_unexpected_request')
        after = app.sql("SELECT md5(string_agg(row_to_json(p)::text,'' ORDER BY id)) FROM public.archive_probe p").strip()
        SCENARIOS.require(after == before, 'restart_changed_archive_probe')
    transitions = app.report['image_transitions']
    SCENARIOS.require([item['role'] for item in transitions] == ['baseline', 'candidate', 'baseline'], 'image_selection_sequence')
    SCENARIOS.require([item['image_id'] for item in transitions] ==
                      [app.image_ids[role] for role in ['baseline', 'candidate', 'baseline']], 'actual_image_id_selection')
    SCENARIOS.require(app.image_ids['baseline'] != app.image_ids['candidate'], 'image_ids_not_distinct')
    return {'owned_local_image_return_verified': True, 'identical_binary': True,
            'cross_version_compatibility_verified': False, 'production_rollback_verified': False,
            'duplicate_server_argument_started': True, 'restart_mutations': observations}


@unittest.skipUnless(os.environ.get('RUN_SYNTHETIC_APPLICATION_TESTS') == '1', 'synthetic application is opt-in')
class SyntheticApplicationFailureTests(SyntheticApplicationBase):
    def test_callback_failure_and_fresh_retry(self):
        failed, output = self.runner('injected-failure')
        with self.assertRaisesRegex(SCENARIOS.ScenarioFailure, '^invented_callback_failure$'):
            with failed.open_application() as app:
                SCENARIOS.configure_thresholds(app)
                app.start_backend(image='baseline')
                raise SCENARIOS.ScenarioFailure('invented_callback_failure')
        self.assert_cleaned(failed, output, False)
        retry, retried_output = self.runner('fresh-retry')
        with retry.open_application() as app:
            SCENARIOS.configure_thresholds(app)
            app.start_backend(image='baseline')
            self.assertEqual(set(app.request('/info')['enabled_chains']), set(SCENARIOS.CHAINS))
            self.assertNotEqual(app.project, failed.project)
            self.assertNotEqual(app.clone_id, failed.clone_id)
        self.assert_cleaned(retry, retried_output, True)


if __name__ == '__main__':
    unittest.main()
