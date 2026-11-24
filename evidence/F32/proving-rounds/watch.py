import re, sys, collections
for p in sys.argv[1:]:
    t = open(p, encoding='utf-8', errors='replace').read().splitlines()
    name = p.split('/')[-1]
    starts = [l for l in t if l.startswith('watch start')]
    caps = [l for l in t if l.startswith('== capture')]
    lv = collections.Counter(re.search(r'levels_below_init=(\d+)', l).group(1) for l in t if 'levels_below_init=' in l)
    lv0 = sorted(set(re.search(r'cg=(\S+)', l).group(1) for l in t if 'levels_below_init=0' in l))
    con = collections.Counter()
    for i, l in enumerate(t):
        if l == '-- conmon':
            j = i + 1
            while t[j].startswith('pid='):
                con[re.search(r'cgroup=(\S+)', t[j]).group(1)] += 1
                j += 1
    lo = [i for i, l in enumerate(t) if l.startswith('== leftover-check')]
    sv = sum(1 for i in lo if 'reason=svc-start' in t[i])
    rc = collections.Counter(); hist = collections.Counter(); named = 0
    for i in lo:
        m = re.search(r'podman ps rc=(\d+) lines=(\d+)', t[i + 1]); rc[m.group(1)] += 1; hist[m.group(2)] += 1
        j = i + 2
        while not t[j].startswith('== end leftover-check'):
            if re.match(r'^(tbound-accept-|tbound-v-)', t[j]): named += 1
            j += 1
    ts = [l.split()[2] for l in caps]
    print(name)
    print('  watch starts:', [s.split()[2] for s in starts], '; first capture', ts[0], '; last capture', ts[-1], '; last leftover check', t[lo[-1]].split()[2])
    print('  captures', len(caps), '; levels_below_init', dict(lv), '; levels 0 =', lv0 or 'none')
    print('  conmon cgroup', dict(con))
    print('  leftover checks', len(lo), '(svc-start %d); podman rc != 0:' % sv, sum(v for k, v in rc.items() if k != '0'), '; lines histogram', dict(hist), '; lines naming a tbound-accept-/tbound-v- container:', named)
    print()
