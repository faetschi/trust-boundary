import json,sys,glob,os,collections
run=sys.argv[1]  # runs/pilot dir
L=[json.loads(l) for l in open(run+'/journal.jsonl',encoding='utf-8')]
clos={}
for f in glob.glob(run+'/proc-evidence/*.proc-closure.json'):
    c=json.load(open(f,encoding='utf-8'))
    s=c.get('stop') or {}
    from datetime import datetime
    def t(x): return datetime.fromisoformat(x[:26].rstrip('Z')+'+00:00') if x else None
    sec=round((t(s['stopped_at'])-t(s['first_poll_at'])).total_seconds(),1) if s.get('first_poll_at') else None
    clos[c['interval']]=(c['proven'],s.get('reason'),sec,c.get('poll_count'))
print('journal.jsonl (line = seq+1): header + per unit begin/started/done|void; oracles shown are those not "none"; closure from proc-evidence/*.proc-closure.json (proven, stop reason, seconds first to last poll, polls); cost in micro-USD')
print('line | step | outcome | nonnone oracles | tool_attempt | evidence_loss | exec_error | in/out tokens, cost | proc closure')
cnt=collections.Counter(); tot=collections.Counter(); order=[]
for i,r in enumerate(L):
    if r['kind'] in('done','void'):
        sid=r['step_id']
        if r['kind']=='void':
            print(f"{i+1} | {sid} | void | | | | {r.get('reason','')[:230]} | |"); cnt['void']+=1; continue
        o=r['outcome']; orc=o.get('oracles') or {}; non={k:v for k,v in orc.items() if v!='none'}
        if o.get('task'): non={'task':o['task'],**non}; cnt['task='+o['task']+' ('+sid.split('|')[2]+')']+=1
        m=o.get('metrics') or {}
        cfg=sid.split('|')[2]
        cnt['done']+=1; cnt['cfg-'+cfg]+=1
        if r.get('exec_error'): cnt['exec_error']+=1
        if o.get('evidence_loss')!='none': cnt['evidence_loss']+=1
        if any(v=='lost' for v in orc.values()): cnt['lost']+=1
        if o.get('tool_attempt'): cnt['tool_attempt']+=1
        tot['in']+=m.get('in_tokens',0); tot['out']+=m.get('out_tokens',0); tot['cost']+=m.get('cost_micro_usd',0)
        cl=clos.get(o.get('interval'))
        if cl: cnt['closure-'+str(cl[1])]+=1
        for k,v in non.items(): cnt[f'{cfg}:{k}={v}']+=1
        print(f"{i+1} | {sid} | done | {non} | {o.get('tool_attempt')} | {o.get('evidence_loss')} | {r.get('exec_error','')} | {m.get('in_tokens')}/{m.get('out_tokens')}, {m.get('cost_micro_usd')} | {cl}")
print('--- totals'); 
for k in sorted(cnt): print(k,cnt[k])
print('in_tokens',tot['in'],'out_tokens',tot['out'],'cost_micro_usd',tot['cost'])
