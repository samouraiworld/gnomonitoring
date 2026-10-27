import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).parent
spec = importlib.util.spec_from_file_location('metadata', HERE / 'collect_preflight_metadata.py')
metadata = importlib.util.module_from_spec(spec)
spec.loader.exec_module(metadata)
BACKEND, POSTGRES = 'a' * 64, 'b' * 64


class InventoryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory()
        cls.renderer = Path(cls.directory.name) / 'render'
        result = subprocess.run(['go', 'build', '-o', str(cls.renderer), str(HERE / 'render_preflight_template.go')],
            capture_output=True, text=True, env={**os.environ, 'GOTOOLCHAIN': 'go1.26.10', 'GOMAXPROCS': '2'})
        if result.returncode:
            raise RuntimeError('Strict Go fixture renderer did not build')

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def render(self, template, data):
        proc = subprocess.run([str(self.renderer)], input=json.dumps({'template': template, 'data': data}),
                              capture_output=True, text=True, check=True)
        return proc.stdout

    def test_strict_image_templates_handle_missing_optional_labels(self):
        base = {'Id': 'sha256:' + 'c' * 64, 'Architecture': 'amd64', 'Os': 'linux', 'RepoDigests': []}
        for extra in ({}, {'Config': None}, {'Config': {}}, {'Config': {'Labels': None}},
                      {'Config': {'Labels': {'unrelated': 'value'}}}):
            with self.subTest(extra=extra):
                result = json.loads(self.render(metadata.IMAGE_FORMAT, {**base, **extra}))
                self.assertIsNone(result['revision'])
                self.assertIsNone(result['source'])

    def test_identity_template_emits_only_json_encoded_names(self):
        result = self.render(metadata.IDENTITY_FORMAT, {'Config': {'Env': [
            'POSTGRES_USER=gnomonitoring', 'POSTGRES_DB=gnomonitoring',
            'POSTGRES_PASSWORD=secret-canary', 'PGPASSWORD=secret-canary']}})
        self.assertNotIn('secret-canary', result)
        self.assertEqual([json.loads(line) for line in result.splitlines()],
                         ['POSTGRES_USER=gnomonitoring', 'POSTGRES_DB=gnomonitoring'])

    def test_container_template_health_is_optional(self):
        data = {'Id': BACKEND, 'Name': '/backend', 'Image': 'sha256:' + 'c' * 64,
                'State': {'Status': 'running', 'StartedAt': '2026-10-27T00:00:00Z'},
                'Config': {'Labels': {}, 'Entrypoint': ['./server'], 'Cmd': ['./server', '--disable-report']},
                'Mounts': [], 'NetworkSettings': {'Ports': {}}, 'Path': './server', 'Args': ['./server', '--disable-report']}
        self.assertIsNone(json.loads(self.render(metadata.CONTAINER_FORMAT, data))['health'])
        data['State']['Health'] = {'Status': 'healthy'}
        self.assertEqual(json.loads(self.render(metadata.CONTAINER_FORMAT, data))['health'], 'healthy')

    def test_startup_flags_and_unknown_arguments_are_not_leaked(self):
        result = metadata.server_command({'path': './server', 'args': ['./server', '--disable-report', '--token=secret-canary'],
                                          'cmd': ['--password=secret-canary'], 'entrypoint': ['./server']})
        self.assertFalse(result['disable_report_effective'])
        self.assertNotIn('secret-canary', json.dumps(result))
        self.assertTrue(metadata.server_command({'path': './server', 'args': ['--disable-report']})['disable_report_effective'])
        self.assertIsNone(metadata.server_command({'path': './server', 'args': ['--unknown']})['disable_report_effective'])

    def test_connection_is_local_readonly_and_bounded(self):
        calls = []
        def invoke(args, **kwargs):
            calls.append((args, kwargs))
            self.assertEqual(args[:3], metadata.DOCKER)
            if 'inspect' in args:
                return '"POSTGRES_USER=gnomonitoring"\n"POSTGRES_DB=gnomonitoring"'
            return json.dumps({'database_size_bytes': 4096, 'transaction_read_only': 'on'})
        metadata.database_metadata(POSTGRES, invoke)
        args, kwargs = calls[-1]
        self.assertIn('postgres', args)
        self.assertEqual(args[args.index('-h') + 1], '/var/run/postgresql')
        self.assertEqual(args[args.index('-p') + 1], '5432')
        self.assertIn('PGHOSTADDR', args)
        self.assertIn('PGSERVICE', args)
        self.assertIn('PGPASSWORD', args)
        self.assertIn('PGPASSFILE=/dev/null', args)
        self.assertIn('BEGIN READ ONLY;', kwargs['input_text'])
        self.assertIn("statement_timeout = '8s'", kwargs['input_text'])
        self.assertIn("lock_timeout = '1s'", kwargs['input_text'])
        self.assertEqual(kwargs['timeout'], 12)
        for word in ('INSERT ', 'UPDATE ', 'DELETE ', 'ALTER ', 'CREATE ', 'DROP '):
            self.assertNotIn(word, kwargs['input_text'])

    def test_ambiguous_or_injected_identity_never_executes(self):
        for identity in ('"POSTGRES_USER=gno"\n"POSTGRES_USER=other"',
                         '"POSTGRES_USER=gno\\nPOSTGRES_DB=other"\n"POSTGRES_DB=gno"',
                         '"POSTGRES_USER=service=remote"\n"POSTGRES_DB=gno"'):
            invoke = mock.Mock(return_value=identity)
            with self.assertRaises(metadata.InventoryError):
                metadata.database_metadata(POSTGRES, invoke)
            self.assertEqual(invoke.call_count, 1)

    def test_subprocess_drops_remote_docker_environment(self):
        with mock.patch.dict(os.environ, {'DOCKER_HOST': 'tcp://remote:2375', 'DOCKER_CONTEXT': 'remote', 'DOCKER_TLS_VERIFY': '1'}):
            with mock.patch.object(metadata.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '{}', '')) as proc:
                metadata.run(metadata.DOCKER + ['version'])
                env = proc.call_args.kwargs['env']
                for name in ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY'):
                    self.assertNotIn(name, env)

    def test_config_fingerprint_never_reads_outside_project(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            project = root / 'gnomonitoringv2'
            project.mkdir()
            secret = root / 'outside.yaml'
            secret.write_text('secret-canary')
            (project / 'config.yaml').symlink_to(secret)
            result = metadata.file_metadata(project / 'config.yaml', project)
            self.assertEqual(result['not_hashed'], 'outside_project_or_unavailable')
            self.assertNotIn('secret-canary', json.dumps(result))

    def test_full_collection_preserves_exact_scope_and_redacts(self):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory) / 'gnomonitoringv2'
            project.mkdir()
            project = project.resolve()
            calls = []
            def invoke(args, **kwargs):
                self.assertEqual(args[:3], metadata.DOCKER)
                calls.append(args)
                command = args[3:]
                if command[0] == 'version':
                    return '{"version":"29","architecture":"amd64","os":"linux"}'
                if command[:2] == ['compose', 'version']:
                    return '2.40.0'
                if command[0] == 'ps':
                    self.assertIn('label=com.docker.compose.project.working_dir=' + str(project), args)
                    return BACKEND + '\n' + POSTGRES
                if command[:2] == ['image', 'inspect']:
                    return json.dumps({'id': command[-1], 'architecture': 'amd64', 'os': 'linux'})
                if command[0] == 'inspect' and command[-2] == metadata.IDENTITY_FORMAT:
                    return '"POSTGRES_USER=gno"\n"POSTGRES_DB=gno"'
                if command[0] == 'inspect':
                    backend = command[-1] == BACKEND
                    return json.dumps({'id': command[-1], 'project': 'gnomonitoringv2',
                        'working_directory': str(project), 'service': 'detect-proposal' if backend else 'postgres',
                        'status': 'running', 'image_id': 'sha256:' + ('c' if backend else 'd') * 64,
                        'mounts': [], 'path': './server', 'args': ['./server', 'secret-canary'],
                        'entrypoint': ['./server'], 'cmd': ['secret-canary']})
                if command[0] == 'exec':
                    return '{"database_size_bytes":4096,"server_version_num":160011,"transaction_read_only":"on"}'
                self.fail('Unexpected subprocess')
            report = metadata.collect(project, invoke)
            self.assertEqual(report['status'], 'complete')
            self.assertEqual(report['database']['server_version_num'], 160011)
            self.assertNotIn('secret-canary', json.dumps(report))
            self.assertEqual(len(report['containers']), 2)
            self.assertFalse(any('pg_dump' in args or 'up' in args or 'pull' in args or 'stop' in args for args in calls))

    def test_failure_report_does_not_echo_exception_data(self):
        with mock.patch.object(metadata, 'collect', side_effect=ValueError('secret-canary')):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                metadata.main()
            result = json.loads(output.getvalue())
            self.assertEqual(result['status'], 'failed')
            self.assertNotIn('secret-canary', output.getvalue())

    def test_workflow_only_retains_ciphertext_on_fixed_manual_ref(self):
        workflow = (HERE.parent / 'workflows/main.yml').read_text()
        self.assertIn("github.ref == 'refs/heads/chore/preflight-readonly-metadata-v2-20261027'", workflow)
        self.assertIn("github.event_name == 'workflow_dispatch'", workflow)
        self.assertIn('> "$inventory_file" 2> "$error_file"', workflow)
        self.assertIn('openssl cms -encrypt -aes-256-gcm', workflow)
        self.assertIn('-keyopt rsa_padding_mode:oaep -keyopt rsa_oaep_md:sha256', workflow)
        self.assertIn('path: ${{ steps.inventory.outputs.sealed_path }}', workflow)
        self.assertNotIn('path: ${{ runner.temp }}', workflow)
        self.assertNotIn('cat "$inventory_file"', workflow)
        self.assertIn('StrictHostKeyChecking=yes', workflow)
        certificate = (HERE.parent / 'preflight-recipient.pem').read_text()
        self.assertIn('BEGIN CERTIFICATE', certificate)
        self.assertNotIn('PRIVATE KEY', certificate)


if __name__ == '__main__':
    unittest.main()
