"""Finite, invented full-process acceptance scenarios; no external inputs."""
from datetime import datetime, timezone
import time

CHAINS = ('synthetic-chain', 'synthetic-peer-chain')
PERIODS = {'last_24h', 'current_week', 'current_month', 'current_year'}
ADDRESSES = ('g1active', 'g1beta', 'g1gamma', 'g1delta')


class ScenarioFailure(Exception):
    pass


def require(condition, reason):
    if not condition:
        raise ScenarioFailure(reason)


def quoted(value):
    return "'" + value.replace("'", "''") + "'"


def utc_reference():
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def configure_thresholds(app):
    # Persisted configuration read by the unchanged binary at startup.
    app.sql("""INSERT INTO public.admin_configs(key,value) VALUES
      ('new_validator_scan_minutes','60'), ('stagnation_first_alert_seconds','3600'),
      ('latency_alert_enabled','false'), ('bft_alert_enabled','false')
      ON CONFLICT(key) DO UPDATE SET value=excluded.value""")


def seed_bootstrap(app, reference):
    """Give the immediate metrics cycle real rows before collection advances."""
    state = app.mock_request('/state')
    require(bool(state['validators']), 'mock_validator_set_empty')
    for chain in CHAINS:
        for validator in state['validators']:
            values = (quoted(chain), quoted(validator['address']), quoted(validator['moniker']),
                      '1', quoted(reference), 'true', 'false', 'false')
            app.sql('INSERT INTO public.daily_participations '
                    '(chain_id,addr,moniker,block_height,date,participated,tx_contribution,proposed) VALUES (' +
                    ','.join(values) + ') ON CONFLICT DO NOTHING')
    return state


def seed_report_contract(app, reference):
    """Replace only invented chains after observing genuine collection.

    RPC tips remain fixed below these heights. One proposer per block and
    equal power give 25 expected proposals each. Two of four members sign.
    Chain B is an hour stale but retains valid last-24h latency samples.
    """
    chains = ','.join(quoted(chain) for chain in CHAINS)
    for table in ('daily_participations', 'daily_participation_agregas', 'addr_monikers', 'alert_logs'):
        app.sql(f'DELETE FROM public.{table} WHERE chain_id IN ({chains})')
    for chain_index, chain in enumerate(CHAINS):
        for index, address in enumerate(ADDRESSES):
            moniker = ('alpha', 'alpha-beta', 'gamma', 'delta')[index]
            app.sql('INSERT INTO public.addr_monikers(chain_id,addr,moniker,voting_power,first_active_block) '
                    f'VALUES ({quoted(chain)},{quoted(address)},{quoted(moniker)},1,10000)')
            lag = ('0' if chain_index == 0 else '900') if index == 0 else ('20' if index == 1 else 'NULL')
            date = quoted(reference) + '::timestamptz' + (" - interval '1 hour'" if chain_index else '')
            signed, late = ('true', 'false') if index < 2 else ('false', 'NULL')
            app.sql('INSERT INTO public.daily_participations '
                    '(chain_id,addr,moniker,block_height,date,participated,tx_contribution,proposed,precommit_lag_ms,late_for_quorum) '
                    f'SELECT {quoted(chain)},{quoted(address)},{quoted(moniker)},10000+h,{date},'
                    f'{signed},false,(h % 4 = {index}),{lag},{late} FROM generate_series(0,99) AS h')
    app.sql('INSERT INTO public.alert_logs '
            '(chain_id,addr,moniker,level,start_height,end_height,skipped,msg,sent_at) VALUES '
            f"({quoted(CHAINS[0])},'g1gamma','gamma','WARNING',10000,10005,false,'invented alert',"
            f"{quoted(reference)}::timestamptz - interval '26 hours')")


def validator_rows(report):
    return {row['addr']: row for row in report['validators']}


def assert_consensus_contract(a, b):
    require(a['chain_id'] == CHAINS[0] and b['chain_id'] == CHAINS[1], 'consensus_chain_selection')
    require(a['cache_ttl_seconds'] == 30, 'consensus_cache_ttl')
    ar, br = validator_rows(a), validator_rows(b)
    require(set(ar) == set(ADDRESSES) == set(br), 'consensus_roster')
    zero = ar['g1active']['precommit_lag']['last_24h']
    require(zero['p50_ms'] == 0 and zero['p90_ms'] == 0 and zero['sample_count'] == 100,
            'measured_zero_must_remain_numeric')
    missing = ar['g1gamma']['precommit_lag']['last_24h']
    require(missing['p50_ms'] is None and missing['p90_ms'] is None and missing['sample_count'] == 0
            and missing['last_sample_at'] is None, 'unmeasured_latency_must_remain_null')
    peer = br['g1active']['precommit_lag']['last_24h']
    require(peer['p50_ms'] == 900 and peer['p90_ms'] == 900 and peer['sample_count'] == 100,
            'latency_units_or_chain_isolation')
    require(a['bft']['available'] is True and abs(a['bft']['margin_percent'] + 100 / 6) < .001,
            'negative_bft_margin_preserved')
    require(a['bft']['online_voting_power'] == 2 and a['bft']['total_voting_power'] == 4,
            'bft_voting_power')
    require(b['bft']['available'] is False and b['bft']['reason'] == 'stale'
            and b['bft']['margin_percent'] is None, 'unavailable_bft_must_not_be_zero')


def assert_health_contract(report):
    rows = {row['addr']: row for row in report}
    require(set(rows) == set(ADDRESSES), 'health_roster')
    require(set(rows['g1active']['periods']) == PERIODS, 'four_report_periods')
    for period in PERIODS:
        active = rows['g1active']['periods'][period]
        require(active['score'] == 100 and active['tier'] == 'Excellent', 'perfect_validator_score')
        require(active['sign_rate'] == 100 and active['proposer_reliability'] == 100,
                'sign_and_proposer_rates')
        require(active['voting_power'] == 1, 'health_voting_power')
    require(rows['g1active']['days_since_last_alert'] is None, 'never_alerted_is_null')
    require(rows['g1gamma']['days_since_last_alert'] == 1, 'last_alert_recency')


def command(app, chat_id, text, fragment):
    before = len(app.mock_request('/state')['messages'])
    app.mock_request('/control', {'action': 'telegram', 'bot': 'validator', 'chat_id': chat_id, 'text': text})
    def find():
        messages = app.mock_request('/state')['messages'][before:]
        return next((m for m in messages if m['bot'] == 'validator' and m['chat_id'] == chat_id
                     and fragment in m['text']), None)
    return app.wait_for(find, timeout=45)


def exercise_application(app):
    """Single bounded nominal/backfill/report/bot acceptance path."""
    reference = utc_reference()
    configure_thresholds(app)
    initial = seed_bootstrap(app, reference)
    app.start_backend(image='baseline')
    require(set(app.request('/info')['enabled_chains']) == set(CHAINS), 'enabled_chains')
    for chain in CHAINS:
        app.wait_for(lambda c=chain: int(app.sql('SELECT COUNT(DISTINCT block_height) FROM public.daily_participations '
                                               f'WHERE chain_id={quoted(c)}').strip()) >= initial['heights'][chain], timeout=120)
    def metrics_complete():
        metrics = app.metrics_text()
        log = app.backend_log_text()
        return all(any(line.startswith('gnoland_validator_participation_rate{') and
                       f'chain="{chain}"' in line for line in metrics.splitlines()) and
                   f'[metrics][{chain}] update complete' in log for chain in CHAINS)
    app.wait_for(metrics_complete, timeout=90)
    require('too many connections' not in app.backend_log_text(), 'application_connection_budget_exhausted')
    require('update failed' not in app.backend_log_text(), 'metrics_update_failed')
    def protocol_cycles():
        counters = app.mock_request('/state')['counters']
        return all(counters.get('telegram.' + bot + '.getMe', 0) > 0 for bot in ('validator', 'govdao')) and all(counters.get('graphql.' + chain + '.events', 0) > 0 and
                   counters.get('websocket.' + chain + '.subscribed', 0) > 0 and
                   counters.get('rpc.' + chain + '.block', 0) > 0 for chain in CHAINS)
    app.wait_for(protocol_cycles, timeout=90)
    app.mock_request('/control', {'action': 'proposal', 'chain': CHAINS[0]})
    app.wait_for(lambda: int(app.sql("SELECT count(*) FROM public.govdaos WHERE chain_id=" +
                                    quoted(CHAINS[0]) + " AND id=702 AND title<>'' AND status_synced=true").strip()) == 1,
                 timeout=90)
    app.assert_no_external_network()

    # A separate >500-block jump exercises the blocking backfill branch.
    start_height = int(initial['heights'][CHAINS[0]])
    target = start_height + 600
    app.mock_request('/control', {'action': 'height', 'chain': CHAINS[0], 'height': target})
    app.wait_for(lambda: int(app.sql('SELECT COALESCE(MAX(block_height),0) FROM public.daily_participations '
                                   f'WHERE chain_id={quoted(CHAINS[0])}').strip()) >= target, timeout=180)
    collected = int(app.sql('SELECT COUNT(DISTINCT block_height) FROM public.daily_participations '
                            f'WHERE chain_id={quoted(CHAINS[0])} AND block_height BETWEEN {start_height + 1} AND {target}').strip())
    require(collected == 600, 'backfill_missing_heights')

    # The fixed mock tips are below all report fixture heights. Collection
    # was proved above; this explicit second fixture tests public contracts.
    reference = utc_reference()
    seed_report_contract(app, reference)
    a_path = '/api/reports/consensus?chain=' + CHAINS[0]
    b_path = '/api/reports/consensus?chain=' + CHAINS[1]
    a, b = app.request(a_path), app.request(b_path)
    assert_consensus_contract(a, b)
    assert_health_contract(app.request('/api/reports/validators?chain=' + CHAINS[0]))
    filtered = app.request(a_path + '&addr=g1active')
    require(len(filtered['validators']) == 1 and filtered['validators'][0]['addr'] == 'g1active', 'exact_address_filter')
    require(len(app.request(a_path)['validators']) == 4, 'filtered_cache_roster_mutated')
    require(not app.request(a_path + '&addr=active')['validators'], 'substring_is_not_api_address')
    response = app.request_response(a_path)
    headers = {key.lower(): value for key, value in response['headers'].items()}
    require(response['status'] == 200 and headers.get('cache-control') == 'no-store', 'consensus_http_cache_header')

    app.sql('UPDATE public.daily_participations SET precommit_lag_ms=500 '
            f"WHERE chain_id={quoted(CHAINS[0])} AND addr='g1active'")
    cached = app.request(a_path)
    require(cached['computed_at'] == a['computed_at'] and
            validator_rows(cached)['g1active']['precommit_lag']['last_24h']['p50_ms'] == 0, 'cache_snapshot_not_stable')
    deadline = time.monotonic() + 40
    while time.monotonic() < deadline:
        fresh = app.request(a_path)
        if fresh['computed_at'] != a['computed_at']:
            break
        time.sleep(1)
    else:
        raise ScenarioFailure('cache_did_not_expire')
    require(validator_rows(fresh)['g1active']['precommit_lag']['last_24h']['p50_ms'] == 500,
            'cache_expiry_did_not_reload_payload')
    require(validator_rows(app.request(b_path))['g1active']['precommit_lag']['last_24h']['p50_ms'] == 900,
            'cache_peer_chain_contaminated')

    card = command(app, 71001, '/validator g1active', '<code>g1active</code>')
    require('[' + CHAINS[0] + ']' in card['text'] and 'Score: <b>100' in card['text'], 'validator_address_card')
    command(app, 71001, '/validator alpha', '<code>g1active</code>')
    ambiguous = command(app, 71001, '/validator alph', '2 validators')
    require('g1active' in ambiguous['text'] and 'g1beta' in ambiguous['text'] and 'Tier:' not in ambiguous['text'],
            'validator_ambiguity_not_listed')
    command(app, 71001, '/setchain chain=' + CHAINS[1], CHAINS[1])
    peer_card = command(app, 71001, '/validator g1active', '<code>g1active</code>')
    require('[' + CHAINS[1] + ']' in peer_card['text'] and 'p50 900' in peer_card['text'], 'validator_chat_chain')
    state = app.mock_request('/state')
    require(state['unexpected'] == 0, 'unexpected_mock_request')
    return {'synthetic_only': True, 'reference_time': reference,
            'worker_collection_verified': True, 'backfill_heights': collected,
            'api_contract_verified': True, 'telegram_card_verified': True,
            'cache_expiry_verified': True, 'metrics_observed': True, 'govdao_websocket_processed': True,
            'mock_counters': state['counters']}
