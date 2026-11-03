"""Render reviewed release sources and validated public input for SSH stdin.

SSH always executes the fixed command `python3 -I -`. Request fields are data,
never remote command text. No SSH credential, API token or runtime config enters
this bundle. Hashes detect accidental bundle corruption; trust comes from the
reviewed main commit and the pinned SSH host identity, not these hashes alone.
"""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import sys

from remote_backend_release import validate_request
from rollout_backend import Refused, write_new


def build_bundle(request, sources=None):
    validate_request(request)
    if sources is None:
        sources = {name: Path(__file__).with_name(name + ".py").read_text()
                   for name in ("rollout_backend", "remote_backend_release")}
    bundle = {"request": request, "sources": {
        name: {"code": source, "sha256": hashlib.sha256(source.encode()).hexdigest()}
        for name, source in sources.items()}}
    encoded = base64.b64encode(json.dumps(bundle).encode()).decode("ascii")
    return '''import base64, hashlib, json, sys, types
bundle = json.loads(base64.b64decode(''' + repr(encoded) + '''))
for name in ('rollout_backend', 'remote_backend_release'):
    item = bundle['sources'][name]
    if hashlib.sha256(item['code'].encode()).hexdigest() != item['sha256']:
        raise SystemExit('bundle_integrity_failed')
    module = types.ModuleType(name)
    sys.modules[name] = module
    exec(compile(item['code'], '<reviewed-' + name + '>', 'exec'), vars(module))
raise SystemExit(sys.modules['remote_backend_release'].main(bundle['request']))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        request = json.load(sys.stdin)
        write_new(args.output, build_bundle(request))
    except (Refused, OSError, ValueError, KeyError, TypeError):
        print("release_bundle_refused", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
