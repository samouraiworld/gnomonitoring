#!/usr/bin/env python3
"""Fixed-scope read-only inventory; stdout must be encrypted by the SSH caller."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess


DOCKER = ['docker', '--host', 'unix:///var/run/docker.sock']

CONTAINER_FORMAT = r'''{"id":{{json .Id}},"name":{{json .Name}},"image_id":{{json .Image}},"status":{{json .State.Status}},"started_at":{{json .State.StartedAt}},"health":{{with index .State "Health"}}{{json .Status}}{{else}}null{{end}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"working_directory":{{json (index .Config.Labels "com.docker.compose.project.working_dir")}},"compose_files":{{json (index .Config.Labels "com.docker.compose.project.config_files")}},"mounts":{{json .Mounts}},"ports":{{json .NetworkSettings.Ports}},"path":{{json .Path}},"args":{{json .Args}},"entrypoint":{{json .Config.Entrypoint}},"cmd":{{json .Config.Cmd}}}'''
IMAGE_FORMAT = r'''{"id":{{json .Id}},"architecture":{{json .Architecture}},"os":{{json .Os}},"repo_digests":{{json .RepoDigests}},"revision":{{with index . "Config"}}{{with index . "Labels"}}{{with index . "org.opencontainers.image.revision"}}{{json .}}{{else}}null{{end}}{{else}}null{{end}}{{else}}null{{end}},"source":{{with index . "Config"}}{{with index . "Labels"}}{{with index . "org.opencontainers.image.source"}}{{json .}}{{else}}null{{end}}{{else}}null{{end}}{{else}}null{{end}}}'''
# Only these non-secret initialization identities can leave Docker inspection.
IDENTITY_FORMAT = r'''{{range .Config.Env}}{{if or (eq (index (split . "=") 0) "POSTGRES_USER") (eq (index (split . "=") 0) "POSTGRES_DB")}}{{println (json .)}}{{end}}{{end}}'''
SQL = """BEGIN READ ONLY;
SET LOCAL statement_timeout = '8s';
SET LOCAL lock_timeout = '1s';
SET LOCAL search_path = pg_catalog;
SET LOCAL timezone = 'UTC';
SELECT json_build_object(
 'transaction_read_only', current_setting('transaction_read_only'),
 'server_version', current_setting('server_version'),
 'server_version_num', current_setting('server_version_num')::integer,
 'database', current_database(), 'role', current_user,
 'role_superuser', (SELECT rolsuper FROM pg_roles WHERE rolname = current_user),
 'database_size_bytes', pg_database_size(current_database()),
 'data_directory', current_setting('data_directory'),
 'unix_socket_directories', current_setting('unix_socket_directories'),
 'port', current_setting('port')::integer,
 'in_recovery', pg_is_in_recovery(),
 'postmaster_started_at', pg_postmaster_start_time(),
 'connections', (SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()),
 'extensions', (SELECT coalesce(json_agg(json_build_object('name',extname,'version',extversion)), '[]'::json) FROM pg_extension),
 'largest_relations', (SELECT coalesce(json_agg(x), '[]'::json) FROM
  (SELECT schemaname, relname, n_live_tup AS estimated_rows,
   pg_total_relation_size(relid) AS total_bytes
   FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 8) x)
);
COMMIT;
"""


class InventoryError(Exception):
    pass


def run(args, input_text=None, timeout=15):
    try:
        clean_env = {key: value for key, value in os.environ.items() if not key.startswith('DOCKER_') and key not in {'BUILDKIT_HOST', 'BUILDX_CONFIG', 'BUILDX_BUILDER'}}
        proc = subprocess.run(args, input=input_text, text=True, capture_output=True, timeout=timeout, check=False, env=clean_env)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise InventoryError('command_unavailable_or_timeout') from exc
    if proc.returncode != 0 or len(proc.stdout) > 1024 * 1024:
        # Do not print stderr or exception repr: either might contain arguments/data.
        raise InventoryError('command_failed_or_output_limit')
    return proc.stdout.strip()


def disk_metadata(path):
    try:
        usage = shutil.disk_usage(path)
        return {'total_bytes': usage.total, 'free_bytes': usage.free}
    except OSError:
        return {'unavailable': True}


def file_metadata(path, project):
    path = Path(path)
    try:
        resolved = path.resolve(strict=True)
        resolved.relative_to(project)
        info = resolved.stat()
        if not stat.S_ISREG(info.st_mode) or info.st_size > 2 * 1024 * 1024:
            return {'path': str(path), 'not_hashed': 'not_small_regular_file'}
        return {'path': str(path), 'size_bytes': info.st_size, 'mode': oct(stat.S_IMODE(info.st_mode)),
                'uid': info.st_uid, 'gid': info.st_gid,
                'sha256': hashlib.sha256(resolved.read_bytes()).hexdigest()}
    except (OSError, ValueError):
        return {'path': str(path), 'not_hashed': 'outside_project_or_unavailable'}


def server_command(metadata):
    known = {'./server', '/app/server', 'server', '--disable-report', '-disable-report',
             '--disable-report=true', '-disable-report=true', '--disable-report=false',
             '-disable-report=false', '--'}
    def redact(values):
        return [value if value in known else '<unrecognized-redacted>' for value in (values or [])]
    args = metadata.get('args') or []
    effective = False
    reason = 'default_or_parsed_flags'
    for arg in args:
        if arg == '--' or not arg.startswith('-'):
            reason = 'parsing_stops_at_nonflag_or_separator'
            break
        if arg in ('--disable-report', '-disable-report', '--disable-report=true', '-disable-report=true'):
            effective = True
        elif arg in ('--disable-report=false', '-disable-report=false'):
            effective = False
        else:
            effective = None
            reason = 'unrecognized_flag_not_interpreted'
            break
    recognized = metadata.get('path') in ('./server', '/app/server', 'server')
    return {'executable': metadata.get('path') if recognized else '<unrecognized-redacted>',
            'args': redact(args), 'entrypoint': redact(metadata.get('entrypoint')),
            'cmd': redact(metadata.get('cmd')), 'disable_report_effective': effective if recognized else None,
            'interpretation': reason if recognized else 'executable_not_recognized'}


def database_metadata(container_id, invoke):
    identities = invoke(DOCKER + ['inspect', '--type', 'container', '--format', IDENTITY_FORMAT, container_id])
    entries = [json.loads(line) for line in identities.splitlines() if line.strip()]
    if len(entries) != 2 or any(not isinstance(entry, str) or '=' not in entry for entry in entries):
        raise InventoryError('database_identity_missing_or_ambiguous')
    pairs = [entry.split('=', 1) for entry in entries]
    values = dict(pairs)
    if len(values) != 2 or set(values) != {'POSTGRES_USER', 'POSTGRES_DB'}:
        raise InventoryError('database_identity_missing_or_ambiguous')
    user, database = values.get('POSTGRES_USER', ''), values.get('POSTGRES_DB', '')
    if not all(re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]{0,62}', value) for value in (user, database)):
        raise InventoryError('database_identity_missing_or_not_simple')
    # One documented local socket/port, no network fallback or password access.
    args = DOCKER + ['exec', '-i', '--user', 'postgres', container_id, 'env',
            '-u', 'PGHOSTADDR', '-u', 'PGSERVICE', '-u', 'PGSERVICEFILE', '-u', 'PGOPTIONS',
            '-u', 'PGPASSWORD', '-u', 'PGPASSFILE', 'PGAPPNAME=gnomonitoring_readonly_preflight',
            'PGCONNECT_TIMEOUT=5', 'psql', '-X', '-q', '-t', '-A', '-w',
            '-h', '/var/run/postgresql', '-p', '5432', '-U', user, '-d', database, '-v', 'ON_ERROR_STOP=1']
    result = json.loads(invoke(args, input_text=SQL, timeout=12))
    if (not isinstance(result, dict) or not isinstance(result.get('database_size_bytes'), int)
            or result.get('transaction_read_only') != 'on'):
        raise InventoryError('database_metadata_invalid')
    result['connection_used'] = {'transport': 'local_unix_socket', 'socket_directory': '/var/run/postgresql', 'port': 5432}
    return result


def collect(project, invoke=run):
    project = Path(project).resolve(strict=True)
    if not project.is_dir() or project.name != 'gnomonitoringv2':
        raise InventoryError('documented_project_unavailable')
    report = {'format_version': 2, 'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'status': 'complete', 'project_directory': str(project), 'containers': [], 'errors': [],
              'project_disk': disk_metadata(project), 'files': []}
    for basename in ('docker-compose.yml', 'docker-compose-prod.yml', 'compose.yaml', 'compose.yml',
                     'config.yaml', 'backend/config_docker.yaml'):
        path = project / basename
        if path.exists():
            report['files'].append(file_metadata(path, project))
    report['docker_server'] = json.loads(invoke(DOCKER + ['version', '--format',
        '{"version":{{json .Server.Version}},"architecture":{{json .Server.Arch}},"os":{{json .Server.Os}}}']))
    report['compose_version'] = invoke(DOCKER + ['compose', 'version', '--short'])
    ids = invoke(DOCKER + ['ps', '--all', '--quiet', '--no-trunc', '--filter',
                  'label=com.docker.compose.project.working_dir=' + str(project)]).splitlines()
    if not ids or len(ids) > 16 or any(not re.fullmatch(r'[a-f0-9]{64}', item) for item in ids):
        raise InventoryError('container_scope_empty_or_invalid')
    projects = set()
    postgres_ids = []
    for container_id in ids:
        metadata = json.loads(invoke(DOCKER + ['inspect', '--type', 'container', '--format', CONTAINER_FORMAT, container_id]))
        if metadata.get('working_directory') != str(project) or metadata.get('id') != container_id:
            raise InventoryError('container_scope_changed')
        projects.add(metadata.get('project'))
        image_id = metadata.get('image_id', '')
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', image_id):
            raise InventoryError('invalid_image_id')
        metadata['image'] = json.loads(invoke(DOCKER + ['image', 'inspect', '--format', IMAGE_FORMAT, image_id]))
        if metadata['image'].get('id') != image_id:
            raise InventoryError('image_identity_changed')
        if metadata.get('service') == 'detect-proposal':
            metadata['startup'] = server_command(metadata)
        for field in ('path', 'args', 'entrypoint', 'cmd'):
            metadata.pop(field, None)
        mounts = []
        for mount in metadata.get('mounts') or []:
            entry = {key: mount.get(key) for key in ('Type', 'Name', 'Source', 'Destination', 'RW')}
            if metadata.get('service') == 'postgres' and mount.get('Source'):
                entry['disk'] = disk_metadata(mount['Source'])
            if metadata.get('service') == 'detect-proposal' and mount.get('Destination') == '/app/config.yaml':
                entry['config_fingerprint'] = file_metadata(mount['Source'], project)
            mounts.append(entry)
        metadata['mounts'] = mounts
        report['containers'].append(metadata)
        if metadata.get('service') == 'postgres' and metadata.get('status') == 'running':
            postgres_ids.append(container_id)
    if len(projects) != 1 or not next(iter(projects)):
        raise InventoryError('ambiguous_compose_project')
    report['compose_project'] = next(iter(projects))
    if len(postgres_ids) != 1:
        raise InventoryError('postgres_scope_not_unique')
    try:
        report['database'] = database_metadata(postgres_ids[0], invoke)
    except (InventoryError, ValueError, TypeError) as exc:
        report['status'] = 'partial'
        report['errors'].append({'area': 'database', 'reason': str(exc) if isinstance(exc, InventoryError) else 'invalid_database_metadata'})
    return report


def main():
    try:
        report = collect(Path.home() / 'gnomonitoringv2')
    except (InventoryError, OSError, ValueError, TypeError, KeyError) as exc:
        report = {'format_version': 2, 'status': 'failed', 'error': str(exc) if isinstance(exc, InventoryError) else 'metadata_collection_failed'}
    print(json.dumps(report, separators=(',', ':')))


if __name__ == '__main__':
    def budget_exceeded(_signum, _frame):
        raise InventoryError('wall_clock_budget_exceeded')
    signal.signal(signal.SIGALRM, budget_exceeded)
    signal.alarm(45)
    main()
    signal.alarm(0)
