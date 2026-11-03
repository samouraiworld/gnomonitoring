import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('backup_metadata', Path(__file__).with_name('backup_metadata.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class MetadataBoundaryTest(unittest.TestCase):
    def test_credential_config_is_not_read(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'secret.env'
            p.write_text('DISCORD_WEBHOOK_URL=secret-sentinel\n')
            with patch.object(Path, 'read_bytes', side_effect=AssertionError('config read')):
                result = m.metadata(p, False)
            self.assertTrue(result['exists'])
            self.assertNotIn('secret-sentinel', json.dumps(result))

    def test_symlink_is_not_followed(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'link'
            p.symlink_to('/does-not-exist')
            with patch.object(Path, 'read_bytes', side_effect=AssertionError('symlink read')):
                result = m.metadata(p, True)
            self.assertTrue(result['symlink'])
            self.assertNotIn('sha256', result)

    def test_empty_prefix_has_no_restore_claim(self):
        result = m.objects('postgres/', lambda args: (0, '{"IsTruncated":false}', ''))
        self.assertEqual(result['matching_object_count'], 0)
        self.assertFalse(result['restore_verified'])

    def test_object_names_and_metadata_are_not_printed(self):
        def runner(args):
            if 'head-object' in args:
                return 0, '{"ContentLength":123,"Metadata":{"token":"secret-sentinel"}}', ''
            return 0, json.dumps({'Contents': [
                {'Key': 'postgres/gnomonitoring-20261103T030000Z.dump.age', 'Size': 123, 'LastModified': '2026-11-03T03:00:00Z'},
                {'Key': 'secret-sentinel', 'Size': 456, 'LastModified': '2026-11-03T04:00:00Z'}]}), ''
        result = m.objects('postgres/', runner)
        self.assertEqual(result['matching_object_count'], 1)
        self.assertTrue(result['newest_head_size_matches_listing'])
        self.assertNotIn('secret-sentinel', json.dumps(result))
        self.assertNotIn('gnomonitoring-20261103', json.dumps(result))

    def test_provider_errors_are_redacted(self):
        state, data = m.aws('head-object', [], lambda args: (1, '', 'AccessDenied secret-sentinel'))
        self.assertEqual(state, {'status': 'access_denied'})
        self.assertIsNone(data)

    def test_writes_are_refused(self):
        for name in ['get-object', 'put-object', 'delete-object', 'put-bucket-lifecycle-configuration']:
            with self.assertRaises(ValueError):
                m.aws(name, [], lambda args: self.fail('provider called'))

    def test_inventory_uses_only_fixed_read_operations(self):
        commands = []
        def runner(args):
            commands.append(args)
            if args[0] == 'crontab':
                return 0, '0 * * * * SECRET=secret-sentinel /usr/local/sbin/pg-backup-scaleway.sh\n', ''
            return 0, '{}', ''
        with patch.object(m.shutil, 'which', return_value='/bin/fake'):
            result = m.inventory(runner, {})
        self.assertNotIn('secret-sentinel', json.dumps(result))
        self.assertTrue(result['read_only'])
        for args in commands:
            self.assertTrue(args[:2] in [['crontab', '-l'], ['docker', 'ps']] or args[:len(m.AWS_PREFIX)] == m.AWS_PREFIX)
        self.assertEqual(result['operator_crontab']['postgres_backup_reference_count'], 1)

    def test_postgres_inspection_contains_only_selected_metadata(self):
        calls=[]
        def runner(args):
            calls.append(args)
            if args[1]=='ps': return 0, 'a'*64+'\n', ''
            return 0, json.dumps({'id':'a'*64,'image':'sha256:'+'b'*64,'status':'running','started_at':'2026-11-01T00:00:00Z','health':'healthy','Env':['KEY=secret-sentinel']}), ''
        result=m.postgres_state(runner)
        self.assertEqual(result['status'],'ok')
        self.assertNotIn('secret-sentinel',json.dumps(result))
        self.assertIn('--filter',calls[0])
        self.assertEqual(calls[1][:2],['docker','inspect'])
        self.assertNotIn('exec',calls[1])

    def test_malformed_container_selection_stops_before_inspect(self):
        calls=[]
        def runner(args):
            calls.append(args)
            return 0,'bad-container-id',''
        result=m.postgres_state(runner)
        self.assertEqual(result['status'],'unexpected_selection')
        self.assertEqual(len(calls),1)

    def test_pagination_is_bounded(self):
        calls=[]
        def runner(args):
            calls.append(args)
            return 0, '{"IsTruncated":true,"NextContinuationToken":"private-token"}', ''
        result=m.objects('postgres/',runner)
        self.assertEqual(len(calls),5)
        self.assertTrue(result['truncated'])
        self.assertNotIn('private-token',json.dumps(result))


if __name__ == '__main__':
    unittest.main()
