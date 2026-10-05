#!/usr/bin/env python3
"""Small installed proof. Only owned providers run; all roots auto-delete."""
import argparse, hashlib, importlib.util, json, os, platform, shutil, subprocess, sys, tempfile
sys.dont_write_bytecode = True
from pathlib import Path

sha = lambda data: hashlib.sha256(data).hexdigest()
repo = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('engine_fixture', repo/'scripts/prove-engine-cli.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)

def main():
    p=argparse.ArgumentParser();p.add_argument('--binary',type=Path,required=True);p.add_argument('--agent-binary',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args()
    binary=a.binary.resolve();agent=a.agent_binary.resolve();records=[]
    with tempfile.TemporaryDirectory(prefix='volley-package-') as temporary:
        base=Path(temporary);install=base/'install';install.mkdir();shutil.copy2(binary,install/'volley')
        entries=['volley.sh','cc-volley','codex-volley']
        for name in entries:shutil.copy2(repo/'packaging'/name,install/name)
        def invoke(name,root,env,args,expected=0,code=None,machine=True,input=None):
            command=[str(install/name),*args]+(['--json'] if machine else [])
            result=subprocess.run(command,cwd=root,env=env,input=input,capture_output=True,timeout=40)
            value=json.loads(result.stdout) if machine else result.stdout.decode()
            assert result.returncode==expected,(command,result.returncode,result.stdout,result.stderr)
            if machine:
                if code:assert value['errors'][0]['code']==code,value
                else:assert value['ok'],value
                if name!='volley':assert value['meta']['entrypoint']==name and value['meta']['exit_semantics']=='legacy',value
            records.append({'argv':command,'exit':result.returncode,'response':value,'stderr_sha256':sha(result.stderr)})
            return value
        for name in entries:
            root,settings,env=fixture.setup(base,'role-'+name,agent)
            before={f.name:sha(f.read_bytes()) for f in install.iterdir()}
            invoke(name,root,env,[],1,machine=False)
            assert before=={f.name:sha(f.read_bytes()) for f in install.iterdir()}
            invoke(name,root,env,['--help'])
            role='codex' if name=='codex-volley' else 'claude'
            preview=invoke(name,root,env,['--config',str(settings),'plan',str(root/'ws')])
            assert preview['data']['settings']['planner']==role,preview
            run=invoke(name,root,env,['--config',str(settings),'run',str(root/'ws')])
            assert run['data']['status']=='approved'
            manifest=json.loads((root/'ws/state/manifest.json').read_bytes());assert manifest['roles']['planner']==role,manifest
            count=len(fixture.launches(root))
            cached=invoke(name,root,env,[str(root/'ws')]);assert cached['data']['run_id']==run['data']['run_id'] and len(fixture.launches(root))==count
            invoke(name,root,env,['status',str(root/'ws')]);assert len(fixture.launches(root))==count
            old=root/'legacy';(old/'state').mkdir(parents=True);(old/'SPEC.md').write_text('# Old\n');(old/'state/run').write_text('old')
            invoke(name,root,env,[str(old)],1,'LEGACY_BOUNDARY_UNVERIFIED');assert not (old/'state/manifest.json').exists()
        root,settings,env=fixture.setup(base,'question',agent,question=True)
        (root/'ws/SPEC.md').unlink();(root/'ws/BRIEF.md').write_text('# Owned brief\nDraft a short note.\n')
        first=invoke('cc-volley',root,env,['--config',str(settings),'run',str(root/'ws')],1,'ANSWER_REQUIRED')
        again=invoke('cc-volley',root,env,['run',str(root/'ws')],1,'ANSWER_REQUIRED')
        assert again['data']['run_id']==first['data']['run_id'] and len(fixture.launches(root))==1
        payload=b'{"text":"exact answer\\n","idempotency_key":"owned-package-answer"}'
        invoke('cc-volley',root,env,['human','answer',str(root/'ws'),'--question-id',first['data']['question_id'],'--from-stdin'],input=payload)
        invoke('cc-volley',root,env,['runs','resume',str(root/'ws')]);assert len(fixture.launches(root))==3
        root,settings,env=fixture.setup(base,'restored',agent)
        settings.write_text(settings.read_text().replace('closing_pass = false','closing_pass = true')+'max_rounds = 2\n')
        (root/'plan.json').write_text(json.dumps({'critiques':['Optional change.\nVERDICT: APPROVE\n','Reject closing change.\nVERDICT: REVISE\n']}))
        final=invoke('volley',root,env,['--config',str(settings),'run',str(root/'ws')]);assert final['data']['final_result']['closing_result']=='rejected_at_cap'
        message=invoke('volley.sh',root,env,[str(root/'ws')],machine=False)
        assert 'They did not review the restored final artifact' in message
        for review in final['data']['final_result']['rejected_review_evidence']:
            assert review['reviewed_spec_hash']!=final['data']['spec_hash'] and review['reviewed_spec_hash'] in message
            assert str(root/'ws'/review['path']) in message and sha((root/'ws'/review['path']).read_bytes())==review['sha256']
        # An owned checkout layout proves the script-directory default safely.
        for name in entries:
            root,settings,env=fixture.setup(base,'checkout-'+name,agent)
            checkout=root/'ws';(checkout/'cmd/volley').mkdir(parents=True);(checkout/'cmd/volley/main.go').write_text('// Owned marker\n');(checkout/'go.mod').write_text('module owned\n');(checkout/'build').mkdir();shutil.copy2(binary,checkout/'build/volley')
            shutil.copy2(repo/'packaging'/name,checkout/name)
            (root/'config/volley').mkdir();shutil.copy2(settings,root/'config/volley/config.toml')
            result=subprocess.run([str(checkout/name)],env=env,cwd=root,capture_output=True,timeout=40)
            assert result.returncode==0,(name,result.stdout,result.stderr)
            records.append({'source_no_args':name,'workspace':str(checkout),'exit':result.returncode,'stdout':result.stdout.decode()})
        artifacts={}
        for f in base.rglob('*'):
            if f.is_file() and not f.is_symlink():artifacts[str(f.relative_to(base))]={'sha256':sha(f.read_bytes()),'bytes':f.stat().st_size}
    report={'case_ids':['A-PACK-01','A-PACK-02','A-PACK-03'],'tier':'A','fixture':'F-INSTALL','target':platform.system()+'/'+platform.machine(),'binary_sha256':sha(binary.read_bytes()),'agent_binary_sha256':sha(agent.read_bytes()),'wrapper_sha256':{n:sha((repo/'packaging'/n).read_bytes()) for n in entries},'records':records,'artifacts':artifacts,'live_calls':0,'owned_roots_removed':True,'passed':True}
    a.output.write_text(json.dumps(report,indent=2)+'\n');print(sha(a.output.read_bytes()))
if __name__=='__main__':main()
