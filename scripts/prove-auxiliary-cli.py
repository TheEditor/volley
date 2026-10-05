#!/usr/bin/env python3
"""Check auxiliary pass flags through the installed native command."""
import argparse
import importlib.util
import json
from pathlib import Path
import sys
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('engine_cli', Path(__file__).with_name('prove-engine-cli.py'))
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--agent-binary', type=Path, required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    args = parser.parse_args()
    binary, agent, base = args.binary.resolve(), args.agent_binary.resolve(), args.evidence.resolve()
    base.mkdir(mode=0o700, parents=True, exist_ok=True)
    cases = []
    for planner in ('claude', 'codex'):
        for persistent in ('true', 'false'):
            # Engine tests cover outcomes. The other combinations check adapter wiring.
            outcomes = ('unchanged', 'confirmed', 'rejected_at_cap', 'skipped_no_round_left', 'disabled') if (planner, persistent) == ('claude', 'false') else ('confirmed',)
            for outcome in outcomes:
                name = f'{planner}-{persistent}-{outcome}'
                root, settings, env = helper.setup(base, name, agent)
                plan = {'critiques': ['An example can help readers.\nVERDICT: APPROVE\n'],
                        'advisory': 'Consider another example.\nVERDICT: REVISE\n',
                        'closing_unchanged': outcome == 'unchanged'}
                cap = 2
                if outcome == 'rejected_at_cap':
                    plan['critiques'].append('Changes need correction.\nVERDICT: REVISE\n')
                if outcome == 'skipped_no_round_left':
                    cap = 1
                (root/'plan.json').write_bytes(helper.canonical(plan))
                settings.write_text(settings.read_text().replace('closing_pass = false', 'closing_pass = '+('false' if outcome == 'disabled' else 'true')))
                flags = ['--planner', planner, '--persistent', persistent, '--second-opinion=true',
                         '--closing-pass', 'false' if outcome == 'disabled' else 'true', '--max-rounds', str(cap)]
                record, envelope = helper.invoke(binary, root, settings, env, ['run', str(root/'ws'), *flags])
                result = envelope['data']['final_result']
                assert result['closing_result'] == outcome, result
                assert result['ordinary_verdict']['value'] == 'APPROVE', result
                assert 'approve implementation' in result['meaning'], result
                ordinary = (root/'ws/SPEC.md').read_bytes()
                assert result['basis']['spec_hash'] == helper.digest(ordinary), result
                before = helper.launches(root)
                resumed, again = helper.invoke(binary, root, settings, env, ['runs', 'resume', str(root/'ws')])
                assert before == helper.launches(root), 'saved result called another agent'
                assert again['data']['final_result'] == result, again
                if outcome == 'rejected_at_cap':
                    assert (root/'ws/rounds/closing-approved.spec.md').read_bytes() == ordinary
                    assert (root/'ws/rounds/closing-rejected.spec.md').read_bytes() != ordinary
                    for review in result['rejected_review_evidence']:
                        assert review['reviewed_spec_hash'] != helper.digest(ordinary), review
                helper.evidence(root, name, [record, resumed], binary, agent)
                cases.append(name)
    for flag in ('--second-opinion', '--closing-pass'):
        grammar = [('missing', [flag]), ('empty', [flag+'=']), ('invalid', [flag+'=yes']),
                   ('case', [flag+'=TRUE']), ('number', [flag+'=1']), ('null', [flag+'=null']),
                   ('repeat', [flag+'=true', flag+'=false']), ('missing-before-flag', [flag, '--max-rounds=2'])]
        for variant, flags in grammar:
            name = flag[2:]+'-'+variant
            root, settings, env = helper.setup(base, name, agent)
            record, _ = helper.invoke(binary, root, settings, env, ['run', str(root/'ws'), *flags], expected=1, code='INVALID_INPUT')
            assert not helper.launches(root), 'invalid grammar launched agent'
            helper.evidence(root, name, [record], binary, agent)
            cases.append(name)
    print(json.dumps({'passed': len(cases), 'cases': cases}))


if __name__ == '__main__':
    main()
