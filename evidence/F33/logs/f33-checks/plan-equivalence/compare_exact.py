import json, sys, collections
def load(p): return json.load(open(p, encoding="utf-8"))
def proof(name, a_path, b_path):
    a, b = load(a_path), load(b_path)
    same_manifest = a["manifest_digest"] == b["manifest_digest"]
    sa, sb = a["steps"], b["steps"]
    identical = sa == sb
    first = next((i for i, (x, y) in enumerate(zip(sa, sb)) if x != y), None)
    print(f"{name}: manifest equal {same_manifest}; steps {len(sa)} vs {len(sb)}; identical step for step (all fields, index included): {identical}"
          + ("" if identical else f"; first difference at {first}"))
    print(f"  units {a['units']} vs {b['units']}; PlannedUnits legacy {a['planned_units_legacy']} vs {b['planned_units_legacy']}; draw-pin path {a['planned_units_drawpin']} vs {b['planned_units_drawpin']}; plan digest equal {a['plan_digest'] == b['plan_digest']}")
    return b
proof("V on (#284) vs F32", f"{sys.argv[1]}/plan-f32.json", f"{sys.argv[1]}/plan-284-v1.json")
b = proof("V off (#284) vs V-free #272", f"{sys.argv[1]}/plan-dev.json", f"{sys.argv[1]}/plan-284-v0.json")
on = load(f"{sys.argv[1]}/plan-284-v1.json")
cmp = collections.OrderedDict()
for s in on["steps"]:
    if s["kind"] == "compliant": cmp.setdefault(s["case_id"], []).append(s["configuration"])
orders = ["".join(v) for v in cmp.values()]
print("V on: compliant orders", dict(collections.Counter(orders)), "B before C:", sum(o.index("B") < o.index("C") for o in orders), "V before B:", sum(o.index("V") < o.index("B") for o in orders))
