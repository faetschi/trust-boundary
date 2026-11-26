"""usage: py -3 -I r-mkrun.py <round dir F:/...> <monitor_seconds> <guest ip> [construct-witness] [only_cases comma list]"""
import hashlib, json, sys
M = sys.argv[1].replace(chr(92), '/'); mon = int(sys.argv[2]); ip = sys.argv[3]
purpose = sys.argv[4] if len(sys.argv) > 4 and sys.argv[4] else None
only = [c for c in sys.argv[5].split(',') if c] if len(sys.argv) > 5 else []
ref = json.load(open('F:/trust-boundary/.local/round-rehearsal-dev-02-f30/run.json'))  # constant fields: admission_policy, evq_obligations
d = json.load(open(f'{M}/vals/ga-run.json'))
vals = dict(l.split('=', 1) for l in open(f'{M}/vals/values.txt').read().splitlines() if '=' in l and not l.startswith(('passed', 'not_', 'unmapped', 'cells', 'complete')))
assert vals['FROZEN-COMMIT'] == '4e8ac37d8f2cbb5a67a8a5c8b965c8345ece4e36', vals['FROZEN-COMMIT']
assert d.get('freeze_commit') == vals['FROZEN-COMMIT'], d.get('freeze_commit')
ev = open(f'{M}/inputs/evq_results.json', 'rb').read()
d.update({
    'manifest': f'{M}/inputs/manifest.json', 'quota_profile': 'pilot', 'evq_obligations': ref['evq_obligations'],
    'artifacts_dir': f'{M}/inputs/artifacts', 'only_cases': only, 'admission_policy': ref['admission_policy'],
    'route_report': f'{M}/inputs/route_report.json',
    'evq_results': {'path': f'{M}/inputs/evq_results.json', 'sha256': 'sha256:' + hashlib.sha256(ev).hexdigest()},
    'ledger': 'synthetic-empty', 'run_dir': f'{M}/runs/pilot',
    'ssh': {'path': 'C:/Program Files/Git/usr/bin/ssh.exe', 'known_hosts': f'{M}/known_hosts', 'host': ip, 'host_key_alias': '172.18.155.19',
            'client': {'user': 'root', 'key': 'F:/trust-boundary/.local/secrets/abi8-forced/cl.key'},
            'evidence': {'user': 'root', 'key': 'F:/trust-boundary/.local/secrets/abi8-forced/ev.key'},
            'trial': {'user': 'root', 'key': 'F:/trust-boundary/.local/secrets/abi8-forced/tr.key'}},
    'proc': {'cell_uid': 1000, 'cell_gid': 1000, 'monitor_seconds': mon, 'teardown_grace_seconds': 30, 'authorized_sockets': [], 'drain_grace_seconds': 10},
})
d['pins']['profile_hash'] = vals['pilot/PROFILE_DIGEST']
d['pins']['containment_profile_digest'] = vals['pilot/TD']
if purpose: d['purpose'] = purpose
json.dump(d, open(f'{M}/run.json', 'w', newline='\n'), indent=2); open(f'{M}/run.json', 'a').write('\n')
print('run.json written', {k: d['pins'][k] for k in ('profile_hash', 'containment_profile_digest', 'runner_digest')}, 'purpose', d.get('purpose'), 'only', only, 'monitor', mon, 'host', ip)
