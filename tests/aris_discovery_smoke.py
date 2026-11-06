#!/usr/bin/env python3
"""Native autonomous CLI lifecycle; Python only creates the synthetic input."""
import argparse
import base64
import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile


def fixture():
    dates=[(datetime.date(2020,1,1)+datetime.timedelta(days=i)).isoformat() for i in range(180)]
    stocks={}; signals=[]
    for code in ('sz000001','sz000002'):
        rows=[]
        for i,date in enumerate(dates):
            price=10000000+(i%8)*10000 if code=='sz000001' else 11000000-(i%8)*20000
            for adjust in ('qfq','none'):
                rows.append([adjust,date,price,price+50000,price-50000,price,1000000+(i%7)*100000,'fixture'])
            if i>=65 and i%8==1: signals.append({'code':code,'date':date,'atr':.04})
        stocks[code]=rows
    signals.sort(key=lambda s:(s['date'],s['code']))
    compressed=gzip.compress(json.dumps({'schema_version':1,'calendar':dates,'stocks':stocks}).encode(),mtime=0)
    config={'capital':200000,'position_fraction':.1,'max_positions':10,'participation':.001,'tick':.000001,'holding_sessions':3,'target':.05,'stop':.05,'slippage':.001,'annual_risk_free':0}
    bundle={'request':{'schema_version':2,'run_id':'discovery-fixture','strategy':'ma-proxy'},'human_contract':'Native synthetic discovery smoke; never production qualification.',
            'contract':{'schema_version':1,'id':'discovery-fixture','human_contract_sha256':'','build_sha256':'','baseline':'base','candidates':['control'],'input_sha256':{},
                        'constraints':{'max_drawdown':.3,'absolute_tail_loss':50000,'min_closed_trades':1,'min_signal_days':1,'min_profit_factor':1,'min_net_expectancy':0,'stress_slippage':[.002]},
                        'budget':{'max_candidates':1,'max_attempts_per_candidate':1,'max_review_calls':0,'timeout':'1m'},
                        'inference':{'block_sessions':20,'min_independent_blocks':4,'bootstrap_samples':100,'confidence':.95,'seed':42},'review':{'mode':'off','require_cross_family':False}},
            'shared':{'schema_version':1,'snapshot_gzip':base64.b64encode(compressed).decode(),'snapshot_sha256':hashlib.sha256(compressed).hexdigest(),'sessions':dates,'config':config,'signals':{'base':signals,'control':signals},'limit_mode':'historical_board'}}
    plan={'schema_version':1,'id':'auto-smoke','train_through':'2020-04-20','validation_from':'2020-05-01','validation_through':'2020-06-28','max_trials':3,'patience':3,'max_terms':2,'timeout':'1m'}
    return bundle,plan


def main():
    ap=argparse.ArgumentParser();ap.add_argument('binary',nargs='?');ap.add_argument('--image');args=ap.parse_args()
    if not args.image and not args.binary: ap.error('binary or --image required')
    with tempfile.TemporaryDirectory(prefix='aris-discovery-') as tmp:
        root=Path(tmp);root.chmod(0o755)
        fixtures=root/'fixtures';fixtures.mkdir();fixtures.chmod(0o755);state=root/'state';state.mkdir();logs=root/'logs';logs.mkdir()
        if args.image:
            for path in (state,logs): os.chown(path,10001,10001)
            prefix=['docker','run','--rm','--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
                    '-v',f'{fixtures}:/input:ro','-v',f'{state}:/state','-v',f'{logs}:/logs',args.image]
            input_root=Path('/input');state_root='/state';logs_root='/logs'
        else:
            prefix=[str(Path(args.binary).resolve())];input_root=fixtures;state_root=str(state);logs_root=str(logs)
        def call(command,*extra,success=True):
            for path in fixtures.iterdir(): path.chmod(0o644)
            r=subprocess.run(prefix+[command,*extra,'--state-dir',state_root,'--log-dir',logs_root,'--json'],capture_output=True,text=True,timeout=90)
            if success and r.returncode!=0: raise AssertionError((command,r.returncode,r.stderr))
            if not success:
                assert r.returncode!=0,(command,'unexpected success');return
            return json.loads(r.stdout)
        bundle,plan=fixture()
        (fixtures/'request.json').write_text(json.dumps(bundle));(fixtures/'plan.json').write_text(json.dumps(plan))
        sealed=call('seal','--request',str(input_root/'request.json'))
        (fixtures/'sealed.json').write_text(json.dumps(sealed))
        result=call('discover','--request',str(input_root/'sealed.json'),'--search-plan',str(input_root/'plan.json'))
        assert result['status'].startswith('completed_') and 0<len(result['trials'])<=3
        assert all(t['status']=='completed' for t in result['trials'])
        assert call('discover-status','--run','auto-smoke')['trials']==result['trials']
        assert call('discover-verify','--run','auto-smoke')['verified']
        call('discover','--request',str(input_root/'sealed.json'),'--search-plan',str(input_root/'plan.json'),success=False)
        call('discover-resume','--run','auto-smoke',success=False)
        call('discover',success=False)
        plan['id']='invalid-overlap';plan['validation_from']=plan['train_through'];(fixtures/'invalid.json').write_text(json.dumps(plan))
        call('discover','--request',str(input_root/'sealed.json'),'--search-plan',str(input_root/'invalid.json'),success=False)
        (state/'campaigns/auto-smoke/trial-000.json').write_text('{}')
        call('discover-verify','--run','auto-smoke',success=False)
        print('Autonomous discovery CLI smoke passed')

if __name__=='__main__': main()
