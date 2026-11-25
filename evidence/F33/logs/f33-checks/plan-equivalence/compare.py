import json, sys, collections
a = json.load(open(sys.argv[1], encoding="utf-8"))   # F32
b = json.load(open(sys.argv[2], encoding="utf-8"))   # F33
def key(st):
    d = dict(st); d.pop("index", None); return json.dumps(d, sort_keys=True)
print("manifest digest equal:", a["manifest_digest"] == b["manifest_digest"], a["manifest_digest"])
for name, d in (("F32", a), ("F33", b)):
    kinds = collections.Counter((s["kind"], s["configuration"]) for s in d["steps"])
    print(f"{name}: units {d['units']} subset {d['subset']} PlannedUnits legacy {d['planned_units_legacy']} draw-pin path {d['planned_units_drawpin']} steps {len(d['steps'])}")
    print(f"  steps by kind/config: {dict(sorted(kinds.items()))}")
# 1. adversarial, ambiguous and variance steps: identical sequence (all fields but index)
for kind in ("adversarial", "ambiguous"):
    sa = [key(s) for s in a["steps"] if s["kind"] == kind]
    sb = [key(s) for s in b["steps"] if s["kind"] == kind]
    print(f"{kind}: F32 {len(sa)} steps, F33 {len(sb)} steps, identical in order and content: {sa == sb}")
adv_idx_a = [s["index"] for s in a["steps"] if s["kind"] == "adversarial"]
adv_idx_b = [s["index"] for s in b["steps"] if s["kind"] == "adversarial"]
print("adversarial step indices identical (they come first in the plan):", adv_idx_a == adv_idx_b)
# 2. compliant: per task, F33 = F32 with V removed (as a set of steps); order rule
ca = collections.OrderedDict(); cb = collections.OrderedDict()
for s in a["steps"]:
    if s["kind"] == "compliant": ca.setdefault(s["case_id"], []).append(s)
for s in b["steps"]:
    if s["kind"] == "compliant": cb.setdefault(s["case_id"], []).append(s)
print("compliant tasks F32/F33:", len(ca), len(cb), "same tasks in the same rank order:", list(ca) == list(cb))
same_steps = all(sorted(key(s) for s in ca[t] if s["configuration"] != "V") == sorted(key(s) for s in cb[t]) for t in ca)
print("each task: F33 steps == F32 steps minus V (all fields but index):", same_steps)
print("F33 has any V step:", any(s["configuration"] == "V" for s in b["steps"]))
orders_b = ["".join(s["configuration"] for s in cb[t]) for t in cb]
orders_a = ["".join(s["configuration"] for s in ca[t] if s["configuration"] != "V") for t in ca]
print("F33 orders:", dict(collections.Counter(orders_b)), "alternating BC/CB by rank:", all(o == ("BC" if i % 2 == 0 else "CB") for i, o in enumerate(orders_b)))
print("F32 orders with V removed:", dict(collections.Counter(orders_a)))
diff = [i for i, (x, y) in enumerate(zip(orders_a, orders_b)) if x != y]
print(f"tasks whose B/C order differs from F32-minus-V: {len(diff)} of {len(orders_b)} (rank positions mod 6: {sorted(set(i % 6 for i in diff))})")
# 3. variance repeats
va = [key(s) for s in a["steps"] if s["role"] == "variance-repeat" or s.get("repeat", 0) > 0]
vb = [key(s) for s in b["steps"] if s["role"] == "variance-repeat" or s.get("repeat", 0) > 0]
print(f"variance repeats: F32 {len(va)} F33 {len(vb)} identical: {va == vb}")
print("F33 totals agree (units == PlannedUnits legacy == draw-pin path == 1835):", b["units"] == b["planned_units_legacy"] == b["planned_units_drawpin"] == 1835)
print("F32 totals (for reference):", a["units"], a["planned_units_legacy"], a["planned_units_drawpin"])
main_a = [s["index"] for s in a["steps"] if s["kind"] == "adversarial" and s.get("repeat", 0) == 0]
main_b = [s["index"] for s in b["steps"] if s["kind"] == "adversarial" and s.get("repeat", 0) == 0]
print(f"main adversarial steps (repeat 0): {len(main_b)}, indices identical: {main_a == main_b}, first {main_b[0]} last {main_b[-1]}")
rep_a = [s["index"] for s in a["steps"] if s.get("repeat", 0) > 0]
rep_b = [s["index"] for s in b["steps"] if s.get("repeat", 0) > 0]
print("variance repeat indices shift by exactly -100 (the removed V steps before them):", all(x - y == 100 for x, y in zip(rep_a, rep_b)) and len(rep_a) == len(rep_b))
amb_a = [s["index"] for s in a["steps"] if s["kind"] == "ambiguous"]
amb_b = [s["index"] for s in b["steps"] if s["kind"] == "ambiguous"]
print("ambiguous indices shift by exactly -100:", all(x - y == 100 for x, y in zip(amb_a, amb_b)))
