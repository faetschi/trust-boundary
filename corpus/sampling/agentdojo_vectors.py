"""AgentDojo injection-vector sets for adaptation rule v1, step 3 (computed once before T1).

For every user task of the pinned v1 suites, AgentDojo's own `get_injection_candidates` runs the task's
ground-truth pipeline on an environment with a canary in every injection vector and lists the vectors whose
canary reaches a tool result. No model is called.

Run with the pinned AgentDojo (commit 089ed468cf3ed0322acc66b0211f26d9d90dbf60) installed:
    <venv python> -I corpus/sampling/agentdojo_vectors.py > corpus/sampling/agentdojo-vectors-v1.json
Output: {"agentdojo_version": ..., "suites": {suite: {user_task_id: [vector, ...]}}}, sorted, LF.
"""
import json
import sys
from importlib.metadata import version

from agentdojo.attacks.base_attacks import BaseAttack
from agentdojo.task_suite.load_suites import get_suites


class _Canaries(BaseAttack):
    """Only the canary environment and `get_injection_candidates` of BaseAttack are used."""

    def attack(self, user_task, injection_task):
        raise NotImplementedError


def main():
    suites = {}
    for name, suite in sorted(get_suites("v1").items()):
        probe = _Canaries(suite, None)
        tasks = {}
        for task_id, task in sorted(suite.user_tasks.items()):
            try:
                tasks[task_id] = sorted(probe.get_injection_candidates(task))
            except ValueError:  # AgentDojo: "<id> is not injectable"
                tasks[task_id] = []
        suites[name] = tasks
    out = {"agentdojo_version": version("agentdojo"), "benchmark_version": "v1", "suites": suites}
    sys.stdout.write(json.dumps(out, indent=1, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
