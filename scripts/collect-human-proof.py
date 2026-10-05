#!/usr/bin/env python3
"""Collect owned T15 file and actual controller-crash evidence."""
import argparse
import hashlib
import json
import pathlib
import re


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--target', required=True)
    parser.add_argument('--log', type=pathlib.Path, required=True)
    parser.add_argument('--root-map', action='append', default=[])
    parser.add_argument('--binary', type=pathlib.Path)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args()
    body = args.log.read_text()
    if not re.search(r'^PASS$', body, re.M) or re.search(r'\bFAIL\b|--- SKIP:', body):
        raise SystemExit('The acceptance log did not pass')
    cases = []
    for original in re.findall(r'owned human evidence ([^\n]+)', body):
        path = original.strip()
        for item in args.root_map:
            old, new = item.split('=', 1)
            if path == old or path.startswith(old + '/'):
                path = new + path[len(old):]
                break
        root = pathlib.Path(path)
        record = root / 'evidence.json'
        # Seam enumeration uses extra fixture roots; only test cases save records.
        if not record.is_file():
            continue
        data = json.loads(record.read_text())
        artifacts = {str(p.relative_to(root)): digest(p) for p in sorted(root.rglob('*'))
                     if p.is_file() and not p.is_symlink()}
        cases.append({'case': data['case'], 'root': root.name,
                      'facts': data['facts'], 'test_binary_sha256': data['test_binary_sha256'], 'artifact_hashes': artifacts})
    sigkills = sum(isinstance(c['facts'], dict) and c['facts'].get('signal') == 'SIGKILL' for c in cases)
    if len(cases) < 70 or sigkills < 40:
        raise SystemExit('The retained T15 case set is incomplete')
    repo = pathlib.Path(__file__).resolve().parent.parent
    sources = [p for folder in ['internal/human', 'internal/store'] for p in (repo / folder).glob('*.go')]
    sources += [repo / 'internal/contract/schemas' / name for name in ['manifest.json', 'receipt.json', 'input-receipt.json']]
    result = {'task': 'T15', 'acceptance_ids': ['A-HUMAN-01', 'A-HUMAN-02', 'A-HUMAN-03', 'A-HUMAN-06'],
              'tier': 'A', 'fixture': 'F-FILES/F-PURE', 'target': args.target,
              'log_sha256': digest(args.log), 'test_results': len(re.findall('--- PASS:', body)),
              'retained_cases': len(cases), 'controller_sigkills': sigkills,
              'source_hashes': {str(p.relative_to(repo)): digest(p) for p in sorted(sources)},
              'cases': cases, 'live_vendor_conversations': 0}
    if args.binary:
        result['binary_sha256'] = digest(args.binary)
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps({'sha256': digest(args.output), 'cases': len(cases), 'sigkills': sigkills}))


if __name__ == '__main__':
    main()
