#!/usr/bin/env python3
"""Read fixed backup metadata only; never invoke a backup, restore or notifier."""
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys

FILES = {
    'backup_script': ('/usr/local/sbin/pg-backup-scaleway.sh', True),
    'row_count_script': ('/usr/local/sbin/pg-row-counts.sh', True),
    'cron_file': ('/etc/cron.d/gnomonitoring-pg-backup', True),
    'backup_config': ('/etc/gnomonitoring/pg-backup.env', False),
    'public_recipients': ('/etc/gnomonitoring/pg-backup.recipients', False),
}
AWS_PREFIX = ['aws', '--endpoint-url', 'https://s3.fr-par.scw.cloud', '--region', 'fr-par', 's3api']
ALLOWED_AWS = {'list-objects-v2', 'head-object', 'get-bucket-lifecycle-configuration', 'get-bucket-versioning'}


def run_command(args):
    try:
        p = subprocess.run(args, capture_output=True, text=True, timeout=18, check=False)
        if len(p.stdout) > 2 * 1024 * 1024 or len(p.stderr) > 65536:
            return 1, '', 'bounded_output_exceeded'
        return p.returncode, p.stdout, p.stderr
    except (OSError, subprocess.TimeoutExpired):
        return 1, '', 'unavailable'


def metadata(path, checksum=False):
    try:
        p = Path(path)
        info = p.lstat()
        result = {'exists': True, 'regular': stat.S_ISREG(info.st_mode),
                  'symlink': stat.S_ISLNK(info.st_mode), 'uid': info.st_uid,
                  'mode': oct(stat.S_IMODE(info.st_mode)), 'bytes': info.st_size,
                  'modified_utc': datetime.datetime.fromtimestamp(info.st_mtime, datetime.timezone.utc).isoformat()}
        if checksum and result['regular'] and info.st_size <= 1024 * 1024:
            result['sha256'] = hashlib.sha256(p.read_bytes()).hexdigest()
        return result
    except FileNotFoundError:
        return {'exists': False}
    except OSError:
        return {'accessible': False}


def aws(operation, arguments, runner):
    if operation not in ALLOWED_AWS:
        raise ValueError('unsupported operation')
    code, out, err = runner(AWS_PREFIX + [operation, '--bucket', 'gnomonitoring', '--output', 'json'] + arguments)
    if code:
        category = 'unavailable'
        for marker, name in [('AccessDenied', 'access_denied'), ('NoSuchBucket', 'missing_bucket'),
                             ('NoSuchLifecycleConfiguration', 'not_configured')]:
            if marker in err:
                category = name
        return {'status': category}, None
    try:
        data = json.loads(out)
        if not isinstance(data, dict):
            raise ValueError('not an object')
        return {'status': 'ok'}, data
    except (ValueError, TypeError):
        return {'status': 'invalid_response'}, None


def objects(prefix, runner):
    valid, foreign, truncated = [], 0, False
    token = None
    for page in range(5):
        args = ['--prefix', prefix, '--max-keys', '1000', '--no-paginate']
        if token:
            args += ['--continuation-token', token]
        state, data = aws('list-objects-v2', args, runner)
        if data is None:
            return state
        entries = data.get('Contents') or []
        if not isinstance(entries, list):
            return {'status': 'invalid_response'}
        for entry in entries:
            if not isinstance(entry, dict):
                continue
            key = entry.get('Key', '')
            matches = isinstance(key, str) and bool(re.fullmatch(r'postgres/gnomonitoring-[0-9]{8}T[0-9]{6}Z\.dump\.age', key))
            if matches and isinstance(entry.get('Size'), int) and isinstance(entry.get('LastModified'), str):
                valid.append(entry)
            else:
                foreign += 1
        truncated = bool(data.get('IsTruncated'))
        if not truncated:
            break
        token = data.get('NextContinuationToken')
        if not isinstance(token, str) or not token:
            return {'status': 'invalid_pagination'}
    result = {'status': 'ok', 'matching_object_count': len(valid),
              'other_object_count': foreign, 'truncated': truncated,
              'content_downloaded': False, 'restore_verified': False}
    if valid:
        newest = max(valid, key=lambda x: x['LastModified'])
        result['newest_modified_utc'] = newest['LastModified']
        result['newest_bytes'] = newest['Size']
        state, head = aws('head-object', ['--key', newest['Key']], runner)
        result['newest_head_status'] = state['status']
        if head is not None:
            result['newest_head_size_matches_listing'] = head.get('ContentLength') == newest['Size']
    return result


def postgres_state(runner):
    args = ['docker', 'ps', '--all', '--quiet', '--no-trunc',
            '--filter', 'label=com.docker.compose.project=gnomonitoringv2',
            '--filter', 'label=com.docker.compose.service=postgres']
    code, out, _ = runner(args)
    if code:
        return {'status': 'unavailable'}
    ids = out.split()
    if not ids:
        return {'status': 'not_found'}
    if len(ids) > 3 or any(not re.fullmatch(r'[0-9a-f]{64}', x) for x in ids):
        return {'status': 'unexpected_selection'}
    template = '{"id":{{json .Id}},"image":{{json .Image}},"status":{{json .State.Status}},"started_at":{{json .State.StartedAt}},"health":{{with .State.Health}}{{json .Status}}{{else}}"not_configured"{{end}}}'
    selected = []
    for container in ids:
        code, out, _ = runner(['docker', 'inspect', '--type', 'container', '--format', template, container])
        if code:
            return {'status': 'unavailable'}
        try:
            data = json.loads(out)
            selected.append({key: data.get(key) for key in ['id', 'image', 'status', 'started_at', 'health']})
        except (ValueError, AttributeError):
            return {'status': 'invalid_response'}
    return {'status': 'ok', 'containers': selected}


def inventory(runner=run_command, files=FILES):
    result = {'format_version': 1, 'observed_at_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'operator_uid': os.getuid(), 'scope': 'documented installation and fixed gnomonitoring/postgres/ target',
              'read_only': True, 'restore_verified': False,
              'tools': {x: shutil.which(x) is not None for x in ['aws', 'docker', 'age', 'flock', 'curl']},
              'files': {name: metadata(path, checksum) for name, (path, checksum) in files.items()}}
    result['postgres'] = postgres_state(runner)
    code, out, _ = runner(['crontab', '-l'])
    if code == 0:
        active = [line for line in out.splitlines() if line.strip() and not line.lstrip().startswith('#')]
        result['operator_crontab'] = {
            'readable': True,
            'postgres_backup_reference_count': sum('pg-backup-scaleway.sh' in x for x in active),
            'legacy_backup_reference_count': sum('backup_chain.sh' in x or 'backup_sqlite' in x for x in active),
        }
    else:
        result['operator_crontab'] = {'readable': False}
    if result['tools']['aws']:
        result['documented_postgres_objects'] = objects('postgres/', runner)
        state, data = aws('get-bucket-versioning', [], runner)
        result['bucket_versioning'] = state
        if data is not None:
            status = data.get('Status', 'not_enabled')
            result['bucket_versioning']['value'] = status if status in ['Enabled', 'Suspended'] else 'not_enabled'
        state, data = aws('get-bucket-lifecycle-configuration', [], runner)
        result['bucket_lifecycle'] = state
        if data is not None:
            rules = data.get('Rules') or []
            result['bucket_lifecycle']['rule_count'] = len(rules)
            result['bucket_lifecycle']['enabled_postgres_expiration_days'] = [
                rule['Expiration']['Days'] for rule in rules if isinstance(rule, dict)
                and rule.get('Status') == 'Enabled'
                and (rule.get('Filter', {}).get('Prefix') == 'postgres/' or rule.get('Prefix') == 'postgres/')
                and isinstance(rule.get('Expiration', {}).get('Days'), int)]
    else:
        result['documented_postgres_objects'] = {'status': 'aws_unavailable'}
    return result


if __name__ == '__main__':
    try:
        print(json.dumps(inventory(), sort_keys=True))
    except Exception:
        # Do not expose raw provider responses, cron lines or config values.
        print(json.dumps({'format_version': 1, 'status': 'inspection_failed', 'read_only': True}))
        sys.exit(1)
