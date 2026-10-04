#!/usr/bin/env python3
"""Collect completed tier-B runs, including independent stub counters."""
import argparse
import hashlib
import json
from pathlib import Path


def sha(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()


def collect(fixture, retained, log):
    lines = log.read_text().splitlines()
    if lines[-1] != "PASS" or any("--- FAIL:" in line or "--- SKIP:" in line for line in lines):
        raise ValueError("Only a complete, non-skipped passing run supplies evidence")
    record = json.loads((fixture / "fixture.json").read_text())
    cases = []
    name = ""
    for line in lines:
        if line.startswith("=== RUN   "):
            name = line.split("=== RUN   ", 1)[1]
        if "owned proof " not in line:
            continue
        original = line.split("owned proof ", 1)[1]
        root = retained / Path(original).name
        summary = json.loads((root / "summary.json").read_text())
        if summary["owned_server_stopped"] is not True:
            raise ValueError("Owned server cleanup is not complete")
        providers = {}
        for provider in ["claude", "codex"]:
            argv = root / "stub" / (provider + ".argv")
            keys = root / "stub" / (provider + ".keys")
            hooks = root / "stub" / (provider + ".hooks")
            app = root / "stub" / (provider + ".app-server")
            raw = argv.read_text() if argv.exists() else ""
            providers[provider] = {
                "agent_processes": raw.count("--settings\n") if provider == "claude" else raw.count("--model\n"),
                "app_server_probes": app.read_text().count("app-server\n") if app.exists() else 0,
                "pastes": keys.read_text().count("<Paste>\n") if keys.exists() else 0,
                "hooks": hooks.read_text().splitlines() if hooks.exists() else [],
            }
        negative = name == "TestIsolationAndTrustRefusals"
        expected = 2 if negative else 1 if name == "TestPinnedContractAndReleaseTriggers" else 2
        if sum(p["agent_processes"] for p in providers.values()) != expected:
            raise ValueError("Independent process count mismatch")
        if sum(p["pastes"] for p in providers.values()) != (0 if negative else expected):
            raise ValueError("Independent paste count mismatch")
        artifacts = {str(p.relative_to(root)): sha(p) for p in sorted(root.rglob("*")) if p.is_file() and not p.is_symlink()}
        ids = ["A-GK-01", "A-GK-02"] if negative else ["A-GK-01"] if name == "TestPinnedContractAndReleaseTriggers" else ["A-GK-02"]
        cases.append({"ids": ids, "tier": "B", "fixture": "F-GK-REAL", "stimulus": name,
                      "owned_root": original, "observed_summary": summary, "independent_provider_counts": providers,
                      "artifact_hashes": artifacts})
    if len(cases) != 10:
        raise ValueError("Expected release case, eight role/placement/trust cases, and refusal case")
    return {"target": record["target"], "fixture_record": record, "log_sha256": sha(log),
            "test_binary_sha256": sha(fixture / "proof-final.test"), "passing_test_results": sum("--- PASS:" in line for line in lines), "cases": cases}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--darwin", type=Path, required=True)
    p.add_argument("--linux", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    a = p.parse_args()
    record = {"task": "T12", "acceptance_ids": ["A-GK-01", "A-GK-02"], "live_vendor_conversations": 0,
              "runs": [collect(a.darwin, a.darwin, a.darwin / "final.log"), collect(a.linux, a.linux / "retained/t12-final", a.linux / "final.log")]}
    record["source_hashes"] = {str(f): sha(f) for f in [Path("scripts/prepare-gashki-fixture.py"), Path("scripts/collect-gashki-proof.py"), *sorted(Path("tests/gashkireal").glob("*.go"))]}
    a.output.write_text(json.dumps(record, indent=2) + "\n")
    print(json.dumps({"output_sha256": sha(a.output), "runs": [{"target": r["target"], "passing_test_results": r["passing_test_results"], "cases": len(r["cases"])} for r in record["runs"]]}))


if __name__ == "__main__":
    main()
