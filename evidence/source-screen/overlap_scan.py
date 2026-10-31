# Cross-source text overlap scan. Reads data files only (json/jsonl/ast.parse); executes nothing from downloads.
import json,re,ast,glob,sys,itertools,collections,pathlib
B=pathlib.Path('F:/TBoundAssets/source-screen-20261031')
items=[]  # (source,id,text)
def add(s,i,t):
    if t and len(t.split())>=4: items.append((s,str(i),t))
for n,fn in enumerate(open(B/'InjecAgent/repo/data/attacker_cases_dh.jsonl',encoding='utf8'),1):
    r=json.loads(fn); add('InjecAgent',f'dh:{n}',r['Attacker Instruction'])
for n,fn in enumerate(open(B/'InjecAgent/repo/data/attacker_cases_ds.jsonl',encoding='utf8'),1):
    r=json.loads(fn); add('InjecAgent',f'ds:{n}',r['Attacker Instruction'])
for p in sorted((B/'AgentDojo/repo/src/agentdojo/default_suites/v1').glob('*/injection_tasks.py')):
    t=ast.parse(p.read_text(encoding='utf8'))
    for nd in t.body:
        if isinstance(nd,ast.ClassDef) and nd.name.startswith('InjectionTask'):
            for b in nd.body:
                if isinstance(b,ast.Assign) and any(getattr(x,'id','')=='GOAL' for x in b.targets):
                    try: g=ast.literal_eval(b.value)
                    except Exception: g=ast.unparse(b.value)
                    add('AgentDojo',f'{p.parent.name}/{nd.name}',g)
for i,fn in enumerate(open(B/'ASB/repo/data/all_attack_tools.jsonl',encoding='utf8')):
    r=json.loads(fn); add('ASB',f'{i}:{r["Attacker Tool"]}',r['Attacker Instruction'])
for r in json.load(open(B/'RiOSWorld/data.json',encoding='utf8')): add('RiOSWorld',r['id'],r['instruction'])
for dom,ids in json.load(open(B/'OS-Harm/repo/evaluation_examples/test_misuse.json',encoding='utf8')).items():
    for i in ids:
        try: add('OS-Harm',f'{dom}/{i}',json.load(open(B/f'OS-Harm/repo/evaluation_examples/examples/{dom}/{i}.json',encoding='utf8')).get('instruction'))
        except Exception: pass
for f in glob.glob(str(B/'RedCode/repo/dataset/RedCode-Exec/py2text_dataset_json/*.json')):
    d=json.load(open(f,encoding='utf8')); add('RedCode',pathlib.Path(f).name,d[0]['Text_summary'])
for r in json.load(open(B/'MSB/repo/data/attack_task.jsonl',encoding='utf8')) if False else []: pass
for fn in open(B/'MSB/repo/data/attack_task.jsonl',encoding='utf8'):
    r=json.loads(fn); add('MSB',r['attack_task'],r['implementation'])
for f in ('test_public','validation'):
    fp=B/f'AgentHarm/benchmark/harmful_behaviors_{"test_public" if f=="test_public" else "validation"}.json'
    seen=set()
    for r in json.load(open(fp,encoding='utf8'))['behaviors']:
        if r['id_original'] in seen: continue
        seen.add(r['id_original']); add('AgentHarm',r['id_original'],r['prompt'])
for r in json.load(open(B/'ToolEmu/repo/assets/all_cases.json',encoding='utf8')): add('ToolEmu',r['name'],r['User Instruction'])
for r in json.load(open(B/'Agent-SafetyBench/repo/data/released_data.json',encoding='utf8')): add('Agent-SafetyBench',r['id'],r['instruction'])
for r in json.load(open(B/'inspect_evals/repo/src/inspect_evals/ipi_coding_agent/dataset/samples.json',encoding='utf8')):
    if not r['is_benign']: add('CodeIPI',r['id'],r['expected_malicious_action'])
for fn in open(B/'WASP/repo/webarena_prompt_injections/configs/croissant/attacks_in_webarena_format.jsonl',encoding='utf8'):
    r=json.loads(fn); add('WASP',r['free_form_name'],r['instruction'])
for r in json.load(open(B/'AIShellJack/payloads.json',encoding='utf8')): add('AIShellJack',r['test_guid'],r['test_name']+' '+r['description'])
for f in glob.glob(str(B/'ControlArena/repo/control_arena/settings/swebench/side_tasks/*.json')):
    r=json.load(open(f,encoding='utf8')); add('ControlArena-SWE',r['instance_id'],r['side_task_description'])
def sh(t):
    w=re.findall(r'[a-z0-9]+',t.lower()); return set(zip(w,w[1:],w[2:]))
S=[sh(t) for _,_,t in items]
print('items',len(items),collections.Counter(s for s,_,_ in items))
hits=[]
for i in range(len(items)):
    for j in range(i+1,len(items)):
        if items[i][0]==items[j][0]: continue
        a,b=S[i],S[j]
        if len(a)<3 or len(b)<3: continue
        jac=len(a&b)/len(a|b)
        if jac>=0.30: hits.append((round(jac,2),items[i][:2],items[j][:2],items[i][2][:90],items[j][2][:90]))
hits.sort(reverse=True)
pc=collections.Counter(tuple(sorted((h[1][0],h[2][0]))) for h in hits)
print(pc)
json.dump(hits,open(B/'overlap_hits.json','w'),indent=0)
for h in hits[:45]: print(h)
