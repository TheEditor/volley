#!/usr/bin/env python3
"""Verify and collect retained T16 owned-process evidence on one target OS."""
import argparse
import hashlib
import json
from pathlib import Path
import re


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def checked_log(path, installed=False):
    text = path.read_text()
    marker = r"^PASS installed CLI:" if installed else r"^PASS$"
    if not re.search(marker, text, re.M) or re.search(r"\bFAIL\b|--- SKIP:|Traceback", text):
        raise SystemExit(f"Acceptance log did not pass: {path.name}")
    return text


def acceptance(name):
    match = re.search(r"TestALOOP(\d\d)", name)
    if match:
        return ["A-LOOP-" + match[1]]
    if "AHUMAN05" in name or name.startswith(("human-", "terminal-")):
        return ["A-HUMAN-05"]
    if name.startswith("signal-"):
        return ["A-LOOP-07"]
    return ["A-LOOP-05", "input-surface grammar"]


def collect(root, expected_binary, target, expected_cli_binary=None):
    cases = []
    for record in sorted(root.glob("*/evidence.json")):
        data = json.loads(record.read_bytes())
        if data["passed"] is not True:
            raise SystemExit("A retained case failed")
        recorded_binary = data.get("test_binary_sha256", data.get("agent_binary_sha256"))
        if recorded_binary != expected_binary:
            raise SystemExit("Retained test binary does not match")
        if data["target_os"].lower() != target.split("/")[0]:
            raise SystemExit("Retained case target OS differs")
        if expected_cli_binary is not None and data["binary_sha256"] != expected_cli_binary:
            raise SystemExit("Retained executable does not match")
        directory = record.parent
        for relative, observed in data["artifact_hashes"].items():
            artifact = directory / relative
            if artifact.is_symlink() or not artifact.is_file() or digest(artifact) != observed["sha256"] or artifact.stat().st_size != observed["bytes"]:
                raise SystemExit(f"Retained artifact changed: {directory.name}/{relative}")
        manifest = directory / "ws/state/manifest.json"
        if manifest.exists():
            try:
                value = json.loads(manifest.read_bytes())
                data["observed_manifest"] = {key: value.get(key) for key in ["run_id", "status", "phase", "round", "max_rounds", "spec_hash", "verdict", "errors", "approval", "question", "current_turn"]}
            except json.JSONDecodeError:
                data["observed_manifest"] = {"valid_json": False, "retained_sha256": digest(manifest)}
        proof = directory / "crash-proof.json"
        if proof.exists():
            data["controller_crash"] = json.loads(proof.read_bytes())
        data["acceptance_ids"] = acceptance(data["case"])
        data["evidence_sha256"] = digest(record)
        data["root"] = directory.name
        cases.append(data)
    return cases


def main():
    parser = argparse.ArgumentParser()
    for name in ("engine-root", "cli-root", "engine-log", "cli-log", "review-log", "engine-binary", "cli-binary", "review-binary", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--target", required=True)
    args = parser.parse_args()
    engine_log = checked_log(args.engine_log)
    checked_log(args.cli_log, installed=True)
    checked_log(args.review_log)
    binary_hash = digest(args.engine_binary)
    engine = collect(args.engine_root, binary_hash, args.target)
    cli = collect(args.cli_root, binary_hash, args.target, digest(args.cli_binary))
    sigkills = sum(c.get("controller_crash", {}).get("signal") == "SIGKILL" for c in engine)
    signals = sum(c["case"].startswith("signal-") for c in cli)
    terminals = sum(c["case"].startswith("terminal-") for c in cli)
    if len(engine) != 201 or len(cli) != 62 or sigkills != 12 or signals != 4 or terminals != 2:
        raise SystemExit(f"Incomplete retained matrix: engine={len(engine)} cli={len(cli)} SIGKILL={sigkills} signals={signals} TTY={terminals}")
    repo = Path(__file__).resolve().parent.parent
    sources = sorted(p for p in (repo / "internal").rglob("*") if p.is_file() and p.suffix in (".go", ".json", ".md") and "PROOF" not in p.name)
    sources += [repo / "cmd/volley/main.go", repo / "go.mod", repo / "go.sum", repo / "scripts/prove-engine-cli.py"]
    result = {"task": "T16", "tier": "A", "target": args.target,
              "acceptance_ids": [f"A-LOOP-{n:02}" for n in range(1, 8)] + ["A-HUMAN-05"],
              "engine_cases": len(engine), "installed_cli_cases": len(cli), "controller_sigkills": sigkills,
              "controller_signals": signals, "owned_tty_cases": terminals, "live_vendor_conversations": 0,
              "test_results": len(re.findall("--- PASS:", engine_log)),
              "binary_hashes": {"engine": binary_hash, "volley": digest(args.cli_binary), "review": digest(args.review_binary)},
              "log_hashes": {path.name: digest(path) for path in [args.engine_log, args.cli_log, args.review_log]},
              "source_hashes": {str(path.relative_to(repo)): digest(path) for path in sources}, "cases": engine + cli}
    args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({key: result[key] for key in ["target", "engine_cases", "installed_cli_cases", "controller_sigkills", "controller_signals", "owned_tty_cases"]} | {"sha256": digest(args.output)}))


if __name__ == "__main__":
    main()
