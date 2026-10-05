#!/usr/bin/env python3
"""Extract reviewed response witnesses; do not call this during tests or CI."""
import argparse
import gzip
import hashlib
import json
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument('--evidence-log', type=Path, required=True)
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
text = args.evidence_log.read_text()
assert '--- FAIL' not in text and '\nPASS\n' in text
witness = json.loads(next(line.split('GOLDEN_EVIDENCE ', 1)[1] for line in text.splitlines() if 'GOLDEN_EVIDENCE ' in line))
roots = witness['roots']

def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':')).encode()

def normalize(value):
    if isinstance(value, str):
        for i, root in enumerate(roots):
            value = value.replace(root, f'/fixture/{i}')
        return value
    if isinstance(value, list):
        return [normalize(v) for v in value]
    if isinstance(value, dict):
        result = {k: normalize(v) for k, v in value.items()}
        if {'ok', 'data', 'meta', 'errors'} <= result.keys():
            result['meta']['elapsed_ms'] = 0
            # The public example uses public fixture paths. Its data hash must
            # match those exact normalized bytes; raw evidence keeps the original.
            result['meta']['data_hash'] = 'sha256:' + hashlib.sha256(canonical(result['data'])).hexdigest()
        return result
    return value

examples = normalize(witness['examples'])
assert '/Users/' not in json.dumps(examples) and '/private/var/folders/' not in json.dumps(examples)
body = canonical(examples) + b'\n'
args.output.mkdir(parents=True, exist_ok=True)
blob = gzip.compress(body, mtime=0)
(args.output / 'goldens.json.gz').write_bytes(blob)
index = {'normalization': ['owned fixture paths', 'envelope elapsed_ms', 'data_hash recomputed after fixture path substitution'], 'sha256': hashlib.sha256(blob).hexdigest(), 'examples': {name: hashlib.sha256(canonical(value)).hexdigest() for name, value in examples.items()}}
(args.output / 'goldens-index.json').write_text(json.dumps(index, sort_keys=True, indent=2) + '\n')
print(len(examples), 'examples;', len(blob), 'compressed bytes')
