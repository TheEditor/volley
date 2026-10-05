#!/usr/bin/env python3
"""Collect owned T13 evidence. Reject failed or skipped acceptance runs."""
import argparse
import hashlib
import json
import pathlib
import re


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--target', required=True)
    parser.add_argument('--fixture', type=pathlib.Path, required=True)
    parser.add_argument('--real-log', type=pathlib.Path, action='append', required=True)
    parser.add_argument('--canned-log', type=pathlib.Path, required=True)
    parser.add_argument('--root-map', action='append', default=[])
    parser.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args()
    maps = [entry.split('=', 1) for entry in args.root_map]
    roots, logs = {}, []
    for kind, paths in [('real', args.real_log), ('canned', [args.canned_log])]:
        for log in paths:
            body = log.read_text()
            if re.search(r'\bFAIL\b|--- SKIP:', body) or not re.search(r'^PASS$', body, re.M):
                raise SystemExit(f'Acceptance log did not pass: {log.name}')
            logs.append({'kind': kind, 'name': log.name, 'sha256': digest(log),
                         'test_results': len(re.findall(r'--- PASS:', body))})
            label = 'owned proof' if kind == 'real' else 'owned canned evidence'
            active = ''
            for line in body.splitlines():
                if line.startswith('=== RUN   '):
                    active = line[len('=== RUN   '):]
                match = re.search(label + r' ([^\n]+)', line)
                if match is None:
                    continue
                original = match[1]
                path = original.strip()
                for old, new in maps:
                    if path == old or path.startswith(old + '/'):
                        path = new + path[len(old):]
                        break
                root = pathlib.Path(path)
                if not root.is_dir():
                    raise SystemExit(f'Missing retained evidence: {root.name}')
                roots[(kind, str(root))] = (root, active)
    fixture = json.loads((args.fixture / 'fixture.json').read_text())
    if fixture['target'] != args.target:
        raise SystemExit('Fixture target does not match')
    binaries = {}
    for name, declaration in fixture['binaries'].items():
        actual = digest(args.fixture / name)
        if actual != declaration['sha256']:
            raise SystemExit(f'Fixture binary changed: {name}')
        binaries[name] = actual
    cases = []
    totals = {'real_roots': 0, 'canned_roots': 0, 'provider_processes': 0,
              'pastes': 0, 'submitted_hooks': 0, 'stop_hooks': 0,
              'controller_sigkills': 0, 'canned_subprocesses': 0}
    for (kind, _), (root, active) in sorted(roots.items()):
        artifacts = {}
        for path in sorted(root.rglob('*')):
            if path.is_file() and not path.is_symlink():
                artifacts[str(path.relative_to(root))] = digest(path)
        counts = {}
        if kind == 'real':
            totals['real_roots'] += 1
            for provider in ['claude', 'codex']:
                stub = root / 'stub'
                read = lambda suffix: (stub / (provider + suffix)).read_text() if (stub / (provider + suffix)).is_file() else ''
                argv, keys, hooks = read('.argv'), read('.keys'), read('.hooks')
                count = {'processes': argv.count('--model\n'), 'pastes': keys.count('<Paste>\n'),
                         'submitted': hooks.count('UserPromptSubmit\n'), 'stops': hooks.count('Stop\n')}
                counts[provider] = count
                for source, target in [('processes', 'provider_processes'), ('pastes', 'pastes'),
                                       ('submitted', 'submitted_hooks'), ('stops', 'stop_hooks')]:
                    totals[target] += count[source]
            crash = root / 'controller-crash-result.json'
            if crash.is_file():
                record = json.loads(crash.read_text())
                if record['signal'] != 'SIGKILL':
                    raise SystemExit('Controller crash did not use SIGKILL')
                totals['controller_sigkills'] += 1
        else:
            totals['canned_roots'] += 1
            records = [json.loads(line) for line in (root / 'calls.jsonl').read_text().splitlines()]
            counts = {'subprocesses': len(records)}
            totals['canned_subprocesses'] += len(records)
        name = json.loads((root / 'case.json').read_text())['test'] if (root / 'case.json').is_file() else active
        cases.append({'kind': kind, 'case': name, 'root': root.name, 'counts': counts, 'artifact_hashes': artifacts})
    if totals['real_roots'] < 34 or totals['canned_roots'] < 40:
        raise SystemExit('Incomplete T13 retained fixture set')
    repo = pathlib.Path(__file__).resolve().parent.parent
    sources = {str(path.relative_to(repo)): digest(path) for folder in ['internal/gashki', 'internal/agent', 'internal/process', 'internal/store', 'tests/gashkireal']
               for path in sorted((repo / folder).rglob('*.go')) if path.is_file()}
    result = {'task': 'T13', 'target': args.target, 'source_commit': fixture['source_commit'],
              'source_archive_sha256': fixture['source_archive_sha256'], 'binaries': binaries,
              'logs': logs, 'totals': totals, 'cases': cases, 'source_hashes': sources,
              'live_vendor_conversations': 0}
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps({'output': args.output.name, 'sha256': digest(args.output), 'totals': totals}))


if __name__ == '__main__':
    main()
