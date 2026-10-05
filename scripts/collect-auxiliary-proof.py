#!/usr/bin/env python3
"""Validate retained auxiliary evidence and bind it to source and binaries."""
import argparse
import hashlib
import json
from pathlib import Path


def digest(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode()


def collect(root, binary_hash, target):
    records = sorted(root.glob('*/evidence.json'))
    assert records, root
    seen = set()
    cases = []
    crashes = 0
    for path in records:
        record = json.loads(path.read_bytes())
        assert record['passed'] is True, path
        assert record['case'] not in seen, record['case']
        seen.add(record['case'])
        assert record['target_os'].lower() == target, (path, record['target_os'])
        key = 'test_binary_sha256' if 'test_binary_sha256' in record else 'binary_sha256'
        assert record[key] == binary_hash, (path, record[key], binary_hash)
        calls = record['calls'] or []
        assert record['owned_agent_launches'] == len(calls), path
        assert all(line.split()[0] in ('claude', 'codex') for line in calls), path
        for rel, item in record['artifact_hashes'].items():
            relative = Path(rel)
            assert not relative.is_absolute() and '..' not in relative.parts, rel
            file = path.parent/relative
            assert file.is_file() and not file.is_symlink(), file
            content = file.read_bytes()
            assert digest(content) == item['sha256'] and len(content) == item['bytes'], file
        proof = path.parent/'crash-proof.json'
        if proof.exists():
            crash = json.loads(proof.read_bytes())
            assert crash['signal'] == 'SIGKILL' and crash['controller_pid'] > 0, crash
            crashes += 1
        cases.append({'case': record['case'], 'evidence_sha256': digest(path.read_bytes()),
                      'owned_agent_launches': record['owned_agent_launches'],
                      'artifact_count': len(record['artifact_hashes'])})
    return {'case_count': len(cases), 'actual_sigkill_count': crashes, 'cases': cases}


def main():
    p = argparse.ArgumentParser()
    for flag in ('engine-root', 'cli-root', 'engine-binary', 'cli-binary', 'engine-log', 'cli-log', 'source', 'output'):
        p.add_argument('--'+flag, type=Path, required=True)
    p.add_argument('--source-record', type=Path)
    p.add_argument('--target', choices=('darwin', 'linux'), required=True)
    a = p.parse_args()
    binaries = {'engine': digest(a.engine_binary.read_bytes()), 'cli': digest(a.cli_binary.read_bytes())}
    for log in (a.engine_log, a.cli_log):
        text = log.read_text()
        assert 'FAIL' not in text and 'SKIP' not in text and 'Traceback' not in text, log
        assert ('PASS' in text if log == a.engine_log else '"passed":' in text), log
    engine = collect(a.engine_root, binaries['engine'], a.target)
    cli = collect(a.cli_root, binaries['cli'], a.target)
    summary = json.loads(a.cli_log.read_text().splitlines()[-1])
    assert summary['passed'] == cli['case_count'], summary
    assert set(summary['cases']) == {c['case'] for c in cli['cases']}, summary
    for path in a.cli_root.glob('*/evidence.json'):
        assert json.loads(path.read_bytes())['agent_binary_sha256'] == binaries['engine'], path
    assert engine['actual_sigkill_count'] > 0, 'Missing interruption evidence'
    final_cases = [c for c in engine['cases'] if c['case'].startswith('TestAFINAL')]
    ids = {f'A-FINAL-{i:02d}': [c['case'] for c in final_cases if c['case'].startswith(f'TestAFINAL{i:02d}')] for i in range(1, 7)}
    assert all(ids.values()), ids
    fixture = a.source/'internal/engine/testdata/optional-approval.md'
    assert fixture.read_text() == 'The design meets the requirements. You can also provide an example for readers.\nVERDICT: APPROVE\n'
    sources = {}
    for part in ('internal', 'cmd', 'prompts', 'scripts', 'docs'):
        for path in sorted((a.source/part).rglob('*')):
            if path.is_file() and '__pycache__' not in path.parts:
                sources[str(path.relative_to(a.source))] = digest(path.read_bytes())
    if a.source_record:
        sources = json.loads(a.source_record.read_bytes())
        for name, expected in sources.items():
            assert digest((a.source/name).read_bytes()) == expected, name
    sources['go.mod'] = digest((a.source/'go.mod').read_bytes())
    sources['go.sum'] = digest((a.source/'go.sum').read_bytes())
    output = {'task': 'T17', 'target_os': a.target, 'binaries': binaries, 'engine': engine, 'installed_cli': cli,
              'acceptance_cases': ids, 'source_hashes': sources,
              'logs': {str(path): digest(path.read_bytes()) for path in (a.engine_log, a.cli_log)},
              'live_conversations': 0, 'approval_meaning': 'Exact bytes under the saved review contract; no implementation approval or runtime proof.'}
    a.output.write_bytes(canonical(output))
    print(json.dumps({'engine_cases': engine['case_count'], 'cli_cases': cli['case_count'], 'actual_sigkill': engine['actual_sigkill_count'], 'sha256': digest(a.output.read_bytes())}))


if __name__ == '__main__':
    main()
