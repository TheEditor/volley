#!/usr/bin/env python3
"""Bounded installed-command proof. Fixtures and launchers auto-delete."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile


def digest(data):
    return hashlib.sha256(data).hexdigest()


def inventory(root):
    records = {}
    for path in sorted(root.rglob('*')):
        name = str(path.relative_to(root))
        if path.is_symlink():
            records[name] = {'kind': 'symlink', 'target': os.readlink(path)}
        elif path.is_file():
            records[name] = {'kind': 'file', 'sha256': digest(path.read_bytes())}
        else:
            records[name] = {'kind': 'directory'}
    return records


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    records = []
    with tempfile.TemporaryDirectory(prefix='volley-migration-proof-') as temporary:
        root = Path(temporary)
        for name in ('home', 'tmp', 'config', 'state', 'data', 'cache', 'runtime', 'tools', 'install'):
            (root / name).mkdir(mode=0o700)
        env = {'HOME': str(root / 'home'), 'TMPDIR': str(root / 'tmp'), 'PATH': str(root / 'tools')}
        env.update({f'XDG_{name.upper()}_HOME': str(root / name) for name in ('config', 'state', 'data', 'cache')})
        env['XDG_RUNTIME_DIR'] = str(root / 'runtime')
        calls = root / 'calls'
        env['F_MIG_CALLS'] = str(calls)
        for name in ('claude', 'codex', 'gashki'):
            tool = root / 'tools' / name
            tool.write_text('#!/bin/sh\necho invoked >> "$F_MIG_CALLS"\nexit 99\n')
            tool.chmod(0o700)
        launchers = {}
        for name in ('volley', 'cc-volley', 'codex-volley'):
            launchers[name] = root / 'install' / name
            launchers[name].symlink_to(binary)

        def invoke(name, argv, expected, code=None):
            result = subprocess.run([str(launchers[name]), *argv, '--json'], env=env, cwd=root, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=10)
            value = json.loads(result.stdout)
            assert result.returncode == expected, (argv, result.returncode, value)
            if code:
                assert value['ok'] is False and value['errors'][0]['code'] == code, value
            else:
                assert value['ok'] is True, value
            records.append({'argv': [name, *argv, '--json'], 'exit': result.returncode, 'response': value, 'stderr_sha256': digest(result.stderr.encode())})
            return value

        for state in ('active', 'completed', 'uncertain'):
            ws = root / state
            (ws / 'state').mkdir(parents=True)
            (ws / 'rounds').mkdir()
            (ws / 'SPEC.md').write_text('# Historical spec\n')
            (ws / 'state' / 'run').write_text('old-' + state)
            (ws / 'state' / 'session.planner').write_text('old-session')
            (ws / 'rounds' / 'r01.critique.md').write_text('VERDICT: APPROVE\n')
            before = inventory(ws)
            for name in launchers:
                argv = ['run', str(ws)] if name == 'volley' else [str(ws)]
                value = invoke(name, argv, 8 if name == 'volley' else 1, 'LEGACY_BOUNDARY_UNVERIFIED')
                assert value['errors'][0]['exit_code'] == 8
                assert 'legacy-report' in value['errors'][0]['remediation']
                if name != 'volley':
                    assert value['meta']['entrypoint'] == name
                assert inventory(ws) == before
            if state == 'completed':
                one = invoke('volley', ['workspace', 'legacy-report', str(ws)], 0)
                two = invoke('volley', ['workspace', 'legacy-report', str(ws)], 0)
                assert one['data'] == two['data']
                assert one['data']['classification'] == 'legacy'
                assert 'not a native approval' in json.dumps(one['data'])
                invoke('volley', ['workspace', 'legacy-report', str(ws), '--yes'], 1, 'UNKNOWN_FLAG')
                invoke('volley', ['workspace', 'migrate', str(ws)], 1, 'UNKNOWN_COMMAND')
                assert inventory(ws) == before
            records.append({'workspace_variant': state, 'artifacts': before, 'unchanged': True})
        collision = root / 'rounds-only'
        (collision / 'rounds').mkdir(parents=True)
        (collision / 'SPEC.md').write_text('# Fresh spec\n')
        (collision / 'rounds' / 'r01.critique.md').write_text('Untrusted old critique\n')
        before = inventory(collision)
        invoke('volley', ['run', str(collision), '--backend=cli', '--claude-bin=' + str(root / 'tools' / 'claude'), '--codex-bin=' + str(root / 'tools' / 'codex'), '--closing-pass=false', '--second-opinion=false'], 5, 'OUTPUT_CONFLICT')
        assert inventory(collision) == before
        records.append({'workspace_variant': 'rounds-only-reserved-collision', 'artifacts': before, 'unchanged': True})
        for name in launchers:
            value = invoke(name, [], 0)
            assert value['data']['default_action'] is None
        assert not calls.exists(), 'A dependency was called'
        assert not list((root / 'state').rglob('*')), 'Command created native state or index'
    proof = {'record_version': 1, 'case_ids': ['A-MIG-01', 'A-MIG-02', 'A-MIG-04'], 'tier': 'A', 'fixture': 'F-FILES', 'target_os': platform.system().lower(), 'binary_sha256': digest(binary.read_bytes()), 'settings': 'Legacy boundary precedes settings resolution; isolated empty roots and stub-only PATH', 'dependency_calls': 0, 'fixtures_retained': False, 'records': records}
    args.output.write_text(json.dumps(proof, indent=2) + '\n')
    print(digest(args.output.read_bytes()))


if __name__ == '__main__':
    main()
