#!/usr/bin/env python3
"""Small installed-command proof. Only owned agent stubs can run."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import tempfile


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--agent-fixture', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--scratch', type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    agent = args.agent_fixture.resolve(strict=True)
    assert shutil.disk_usage(args.scratch or tempfile.gettempdir()).free > 2 * 1024**3
    records = []
    with tempfile.TemporaryDirectory(prefix='volley-cli-proof-', dir=args.scratch) as temporary:
        root = Path(temporary).resolve()
        for name in ('home', 'tmp', 'config', 'state', 'data', 'cache', 'runtime', 'tools', 'capture', 'install'):
            (root / name).mkdir(mode=0o700)
        env = {'HOME': str(root / 'home'), 'TMPDIR': str(root / 'tmp'), 'PATH': str(root / 'tools'), 'TERM': 'dumb', 'NO_COLOR': '1', 'SOURCE_DATE_EPOCH': '100'}
        env.update({f'XDG_{name.upper()}_HOME': str(root / name) for name in ('config', 'state', 'data', 'cache')})
        env.update(XDG_RUNTIME_DIR=str(root / 'runtime'), F_ENGINE_CAPTURE=str(root / 'capture'), F_ENGINE_PLAN=str(root / 'plan.json'))
        (root / 'plan.json').write_text(json.dumps({'critiques': ['VERDICT: REVISE\n', 'VERDICT: APPROVE\n']}))
        for provider in ('claude', 'codex'):
            tool = root / 'tools' / provider
            tool.write_text('#!/bin/sh\nexec ' + shlex.quote(str(agent)) + ' -test.run=^TestEngineAgentChild$ -- --engine-child ' + provider + ' "$@"\n')
            tool.chmod(0o700)
        launchers = {}
        for name in ('volley', 'cc-volley', 'codex-volley'):
            launchers[name] = root / 'install' / name
            launchers[name].symlink_to(binary)

        def call(argv, code=None, entry='volley', raw=False):
            command = [str(launchers[entry]), *argv]
            if not raw:
                command.append('--json')
            result = subprocess.run(command, cwd=root, env=env, stdin=subprocess.DEVNULL, capture_output=True, timeout=15)
            if raw:
                assert result.returncode == 0, result.stderr
                value = None
            else:
                value = json.loads(result.stdout)
                assert set(value) == {'ok', 'tool_version', 'data', 'meta', 'warnings', 'commands', 'errors'}, value
                assert value['ok'] == (code is None), value
                assert value['meta']['ts_iso'] == '1970-01-01T00:01:40Z'
                if code:
                    assert value['errors'][0]['code'] == code, value
                    assert value['errors'][0]['message'].encode() in result.stderr
                else:
                    assert result.returncode == 0, value
                if entry != 'volley':
                    assert value['meta']['exit_semantics'] == 'legacy'
            records.append({'argv': [entry, *argv], 'exit': result.returncode, 'response': value, 'stdout_sha256': digest(result.stdout), 'stderr_sha256': digest(result.stderr)})
            return value, result.stdout

        _, text = call(['-h'], raw=True)
        assert len(text.splitlines()) <= 30 and b'capabilities' in text
        cap, _ = call(['capabilities'])
        assert len(cap['data']['commands']) == 28
        call(['schema', 'envelope'])
        guide, _ = call(['robot-docs', 'guide'])
        assert all('D0' + str(i) in guide['data']['guide'] for i in range(1, 7))
        call(['--bad', 'runs', 'bad'], 'UNKNOWN_FLAG')
        call(['runs', 'bad'], 'UNKNOWN_COMMAND')
        call(['plan', 'ws', '--max-rounds', '--json'], 'MISSING_REQUIRED')
        call(['plan', 'ws', '--json=false'], 'INVALID_INPUT')
        call(['statu'], 'UNKNOWN_COMMAND')
        (root / 'statu').mkdir()
        call(['statu'], 'MISSING_REQUIRED')
        call(['run', './status'], 'MISSING_REQUIRED')
        # Machine selector is before the literal boundary; the suffix is data.
        _, literal = call(['plan', '--json', '--', './status'], raw=True)
        assert json.loads(literal)['data']['workspace'] == str(root / 'status')
        call(['config', 'show', '--persistent', 'false', '--rubric='])
        cfg = root / 'source.toml'
        cfg.write_text('max_rounds = 3\n')
        call(['config', 'set', 'max_rounds', '4', '--config', str(cfg)])
        got, _ = call(['config', 'get', 'max_rounds', '--config', str(cfg)])
        assert '4' in json.dumps(got['data'])
        call(['config', 'validate', str(cfg)])
        call(['config', 'show', '--toml', '--json'], 'INVALID_INPUT')
        target = root / 'shown.toml'
        receipt, _ = call(['config', 'show', '--config', str(cfg), '--toml', '--deliver=file:' + str(target)])
        assert receipt['data']['sha256'] == digest(target.read_bytes())
        call(['config', 'show', '--config', str(cfg), '--deliver=file:' + str(cfg), '--force'], 'OUTPUT_CONFLICT')
        call(['config', 'show', '--deliver=webhook:' + str(root / 'absent')], 'UNKNOWN_DELIVERY_SCHEME')
        assert not (root / 'absent').exists()
        for text in ('same text', 'same text', 'changed text'):
            call(['feedback', text, '--idempotency-key=k'], 'IDEMPOTENCY_CONFLICT' if text == 'changed text' else None)
        # A preserved seed needs a mutable copy, then one revision and approval.
        seed = root / "seed with spaces; ' quote.md"
        seed.write_text('# Original seed\n')
        seed_hash = digest(seed.read_bytes())
        ws = root / 'review'
        value, _ = call(['run', str(ws), '--seed', str(seed), '--closing-pass=false', '--claude-bin', str(root / 'tools/claude'), '--codex-bin', str(root / 'tools/codex')])
        assert value['data']['status'] == 'approved', value
        assert digest(seed.read_bytes()) == seed_hash
        launches = (root / 'capture/launches.txt').read_text().splitlines()
        assert launches == ['codex critic critique', 'claude planner revision', 'codex critic critique'], launches
        status, _ = call(['status', str(ws)])
        assert status['data']['status'] == 'approved'
        call(['doctor', '--workspace', str(ws)])
        for verb in ('plan', 'status'):
            output = root / (verb + '.delivered.json')
            receipt, _ = call([verb, str(ws), '--deliver=file:' + str(output)])
            assert receipt['data']['sha256'] == digest(output.read_bytes())
            assert receipt['data']['bytes'] == output.stat().st_size
        # Completed attachment through native wrapper starts no new turn.
        call([str(ws)], entry='cc-volley')
        assert (root / 'capture/launches.txt').read_text().splitlines() == launches
        artifacts = {str(p.relative_to(root)): digest(p.read_bytes()) for p in sorted(root.rglob('*')) if p.is_file() and not p.is_symlink()}
        assert sum(p.stat().st_size for p in root.rglob('*') if p.is_file() and not p.is_symlink()) < 512 * 1024**2
        report = {'case_ids': ['A-CLI-03', 'A-CLI-04', 'A-CLI-05', 'A-CLI-06', 'A-CLI-07', 'A-MIG-02-installed'], 'tier': 'A', 'os': platform.system(), 'binary_sha256': digest(binary.read_bytes()), 'agent_fixture_sha256': digest(agent.read_bytes()), 'observations': records, 'artifact_hashes': artifacts, 'owned_turns': launches, 'live_vendor_calls': 0, 'fixture_cleanup': 'automatic'}
        args.output.write_text(json.dumps(report, sort_keys=True, indent=2) + '\n')


if __name__ == '__main__':
    main()
