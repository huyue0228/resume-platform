"""Design-only reference calculation. Does not call a model, DB, or project runtime.

Run with Python 3 from any directory. Fixtures compress synthetic input defaults;
they are not generated public contracts. Hand-authored expected results are the
oracle. Database transitions, RBAC, provenance, and production performance are
outside this check.
"""

import copy
import itertools
import json
from pathlib import Path


def calculate(case):
    """Pure reference for section 6; business_metadata is never projected."""
    demands = {}
    for raw in case["demands"]:
        demand = {
            "is_active": True,
            "reception_state": "receiving",
            "required_tags": ["cad"],
            "preferred_tags": ["design"],
            "priority": 1,
            "recent_supply_count": 0,
            "last_allocation_sequence": 0,
            **raw,
        }
        assert demand["id"] not in demands, "duplicate demand"
        demands[demand["id"]] = demand

    current_supply = {key: d["recent_supply_count"] for key, d in demands.items()}
    sequence = {key: d["last_allocation_sequence"] for key, d in demands.items()}
    next_sequence = max(sequence.values(), default=0) + 1
    candidates = sorted(case["candidates"], key=lambda c: (c.get("created_order", 0), c["id"]))
    assert len({c["id"] for c in candidates}) == len(candidates), "duplicate candidate"
    outcomes = []
    for candidate in candidates:
        supported = set()
        for raw in candidate.get("tags", [{"code": "cad"}, {"code": "design"}]):
            tag = {"status": "supported", "confidence_bp": 9000,
                   "source": "model", "evidence_verified": True, **raw}
            if (tag["status"] == "supported" and tag["evidence_verified"]
                    and (tag["source"] == "manual" or tag["confidence_bp"] >= 8000)):
                supported.add(tag["code"])

        mapped = [d for key, d in demands.items()
                  if key in candidate["allowed"] and d["is_active"]]
        receiving = [d for d in mapped if d["reception_state"] == "receiving"]
        options = []
        for demand in receiving:
            required, preferred = set(demand["required_tags"]), set(demand["preferred_tags"])
            if not required.issubset(supported) or not ((required | preferred) & supported):
                continue
            key = demand["id"]
            options.append(((-len(preferred & supported), demand["priority"],
                             current_supply[key], sequence[key], key), key))

        if options:
            _, selected = min(options)
            current_supply[selected] += 1
            sequence[selected] = next_sequence
            next_sequence += 1
            outcomes.append({"member_id": candidate["id"], "demand_id": selected})
        else:
            reason = ("no_active_mapping" if not mapped else
                      "no_receiving_demand" if not receiving else "required_tags_unavailable")
            outcomes.append({"member_id": candidate["id"], "wait": reason})
    return outcomes


def main():
    data = json.loads(Path(__file__).with_name("two-agent-allocation-cases.json").read_text())
    assert data["status"] == "DESIGN_ONLY_NOT_A_PUBLISHED_PROTOCOL"
    failures = []
    mutation_checks = 0
    for case in data["cases"]:
        if calculate(case) != case["expected"]:
            failures.append({"case": case["id"], "actual": calculate(case), "expected": case["expected"]})
        # A published/private flag and any HC quantity must leave allocation unchanged.
        for hc, public in itertools.product([0, 1, 100], [False, True]):
            changed = copy.deepcopy(case)
            changed["business_metadata"] = {str(d["id"]): {"headcount": hc, "is_public": public}
                                            for d in case["demands"]}
            mutation_checks += 1
            if calculate(changed) != case["expected"]:
                failures.append({"case": case["id"], "mutation": "HC/publication affected outcome"})
        # Same facts in a different transport ordering must preserve the plan.
        changed = copy.deepcopy(case)
        changed["candidates"].reverse()
        changed["demands"].reverse()
        mutation_checks += 1
        if calculate(changed) != case["expected"]:
            failures.append({"case": case["id"], "mutation": "transport order affected outcome"})

    result = {"evidence_kind": "design_reference_only", "status": "fail" if failures else "pass",
              "cases": len(data["cases"]), "mutation_checks": mutation_checks,
              "failures": failures,
              "not_tested": ["production code", "database concurrency", "protocol validation",
                             "RBAC", "real-model screening", "business effectiveness"]}
    print(json.dumps(result, ensure_ascii=False, indent=2))
    raise SystemExit(1 if failures else 0)


if __name__ == "__main__":
    main()
