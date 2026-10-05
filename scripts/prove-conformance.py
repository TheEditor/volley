#!/usr/bin/env python3
"""Check the contract profile of one built artifact. No vendor calls."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument('--binary', type=Path, required=True)
parser.add_argument('--profile', choices=('full-ci', 'release-self-check'), required=True)
parser.add_argument('--output', type=Path, required=True)
parser.add_argument('--scratch', type=Path)
args = parser.parse_args()
binary = args.binary.resolve(strict=True)
assert shutil.disk_usage(args.scratch or tempfile.gettempdir()).free > 2 * 1024**3
observations = []
with tempfile.TemporaryDirectory(prefix='volley-contract-', dir=args.scratch) as temporary:
    root = Path(temporary)
    for name in ('home', 'tmp', 'config', 'state', 'tools'):
        (root / name).mkdir(mode=0o700)
    env = {'HOME': str(root / 'home'), 'TMPDIR': str(root / 'tmp'), 'PATH': str(root / 'tools'), 'XDG_CONFIG_HOME': str(root / 'config'), 'XDG_STATE_HOME': str(root / 'state'), 'TERM': 'dumb', 'NO_COLOR': '1', 'SOURCE_DATE_EPOCH': '100'}
    def call(argv, extra=None):
        result = subprocess.run([str(binary), '--json', *argv], env=env | (extra or {}), cwd=root, stdin=subprocess.DEVNULL, capture_output=True, timeout=30)
        value = json.loads(result.stdout)
        assert set(value) == {'ok', 'tool_version', 'data', 'meta', 'warnings', 'commands', 'errors'}
        observations.append({'argv': argv, 'extra_env': extra or {}, 'exit': result.returncode, 'response': value, 'stdout_sha256': hashlib.sha256(result.stdout).hexdigest(), 'stderr_sha256': hashlib.sha256(result.stderr).hexdigest()})
        if value['errors']:
            assert value['errors'][0]['message'].encode() in result.stderr
        return result.returncode, value
    exit_code, first = call(['conformance'])
    assert exit_code == 0 and first['ok'], first
    data = first['data']
    assert data['profile'] == args.profile and data['counts']['fail'] == 0
    results = data['results']
    assert [r['id'] for r in results] == sorted({r['id'] for r in results})
    assert len({r['request_id'] for r in results}) == len(results)
    for row in results:
        assert set(row['target']) == {'verb', 'flag', 'stage', 'shape', 'node'}
        assert row['reason'] and row['verdict'] in ('pass', 'not_applicable')
        assert row['id'].split('::')[1:] == sorted(row['id'].split('::')[1:])
        if row['id'].startswith(('S-01', 'S-02')):
            assert row['verdict'] == ('pass' if args.profile == 'full-ci' else 'not_applicable')
            if args.profile != 'full-ci':
                assert row['reason'] == 'release-build-fault-trigger-unavailable'
    assert ('X-03' in {r['id'] for r in results}) == (args.profile == 'release-self-check')
    _, capabilities = call(['capabilities'])
    reads = capabilities['data']['environment_reads']
    assert any(r['name'] == 'VOLLEY_TEST_FAULT' and r['scope'] == 'test-only' for r in reads) == (args.profile == 'full-ci')
    exit_code, fault = call(['--help'], {'VOLLEY_TEST_FAULT': 'entry'})
    if args.profile == 'full-ci':
        assert exit_code == 6 and not fault['ok'] and fault['errors'][0]['code'] == 'INTERNAL'
    else:
        assert exit_code == 0 and fault['ok'] and not fault['errors']
    _, second = call(['conformance'], {'SOURCE_DATE_EPOCH': '200'})
    assert first['data'] == second['data'] and first['meta']['data_hash'] == second['meta']['data_hash']
    assert first['meta']['ts_iso'] == '1970-01-01T00:01:40Z' and second['meta']['ts_iso'] == '1970-01-01T00:03:20Z'
    assert sum(p.stat().st_size for p in root.rglob('*') if p.is_file()) < 512 * 1024**2
    report = {'case_ids': ['A-CONTRACT-02', 'A-CONTRACT-03', 'A-CONTRACT-04', 'A-CONTRACT-05'], 'tier': 'A', 'fixture': 'F-BUILD/F-PURE', 'target_os': platform.system(), 'profile': args.profile, 'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'observations': observations, 'counts': data['counts'], 'fixture_cleanup': 'automatic', 'live_vendor_calls': 0}
    args.output.write_text(json.dumps(report, sort_keys=True, indent=2) + '\n')
    print(args.profile, 'passed', data['counts'])
