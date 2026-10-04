#!/usr/bin/env python3
"""Capture the pinned Bash matrix with owned stubs and disposable roots.

This is a development fixture builder, not part of the Volley executable.
It never invokes a vendor executable. The pinned source is copied from Git.
"""
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

REPO = Path(__file__).resolve().parent.parent
PIN = "a7252e474023f986d9912469b1e58742e644afc8"
DEST = REPO / "tests/scenarios"


def sha(data):
    return hashlib.sha256(data).hexdigest()


def target(label):
    """Map each observed assertion to the acceptance owner for its behavior."""
    s = label.lower()
    if "guard:" in s and any(x in s for x in ("api", "auth", "anthropic", "openai")):
        return ["A-REC-03"], ["C14"]
    if "closing" in s or "second opinion" in s:
        return ["A-FINAL-01", "A-FINAL-02", "A-FINAL-05"], ["C06", "C07"]
    if "question" in s or "human" in s or "answer" in s:
        return ["A-HUMAN-01", "A-HUMAN-02", "A-HUMAN-03", "A-LOOP-03"], ["C02", "C08"]
    if "review rule" in s or "constraints" in s or "profile" in s:
        return ["A-PROMPT-01", "A-PROMPT-02"], []
    if "skills" in s or "dontask" in s or "tools" in s:
        return ["A-PROMPT-02", "A-DIRECT-05", "A-GK-13"], ["C15"]
    if "context" in s:
        return ["A-CFG-05", "A-REC-02", "A-LOOP-07"], ["C15"]
    if "model" in s or "effort" in s or "provenance" in s or "1m" in s:
        return ["A-REC-01", "A-DIRECT-01", "A-GK-01"], ["C12"]
    if "persistent" in s or "session" in s:
        return ["A-DIRECT-02", "A-DIRECT-03", "A-DIRECT-04"], ["C10"]
    if "send exit" in s or "no resend" in s:
        return ["A-GK-05", "A-GK-06", "A-GK-09"], ["C09", "C11"]
    if "timeout" in s or "no limit" in s:
        return ["A-PROC-01", "A-GK-07", "A-GK-08"], ["C03", "C11"]
    if "critic edits" in s:
        return ["A-GK-13", "A-LOOP-07"], ["C15"]
    if "resume" in s or "pin" in s or "other window" in s or "original window" in s:
        return ["A-LOOP-06", "A-GK-10", "A-GK-11", "A-REC-02"], ["C01", "C09", "C10", "C11", "C12"]
    if "gashki" in s:
        return ["A-GK-01", "A-GK-02", "A-GK-03", "A-GK-04", "A-GK-12"], ["C09", "C11", "C14", "C15"]
    if "verdict" in s or "re-ask" in s:
        return ["A-LOOP-02", "A-FINAL-04"], ["C04", "C05"]
    if "impasse" in s:
        return ["A-LOOP-05"], ["C01"]
    if "seed" in s:
        return ["A-LOOP-02"], []
    if "setup" in s:
        return ["A-CLI-02", "A-CFG-04"], ["C13"]
    return ["A-LOOP-01", "A-LOOP-02", "A-DIRECT-01"], ["C01"]


def main():
    DEST.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="volley-baseline-") as tmp:
        root = Path(tmp)
        src = root / "source"
        src.mkdir()
        paths = subprocess.check_output(
            ["git", "ls-tree", "-r", "--name-only", PIN], cwd=REPO, text=True
        ).splitlines()
        files = [p for p in paths if p in ("volley.sh", "README.md", "cc-volley", "codex-volley", "tests/run-matrix.sh") or p.startswith(("prompts/", "tests/mocks/"))]
        hashes = {}
        for p in files:
            data = subprocess.check_output(["git", "show", f"{PIN}:{p}"], cwd=REPO)
            f = src / p
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_bytes(data)
            hashes[p] = sha(data)
            if p in ("volley.sh", "cc-volley", "codex-volley", "tests/run-matrix.sh") or p.startswith("tests/mocks/"):
                f.chmod(0o755)
        matrix = src / "tests/run-matrix.sh"
        code = matrix.read_text()
        code = code.replace('ok()  { echo "PASS: $*";', 'ok()  { printf "%s\\t%s\\tpass\\n" "${WS:-}" "$*" >>"$BASELINE_MAP"; echo "PASS: $*";')
        code = code.replace('bad() { echo "FAIL: $*";', 'bad() { printf "%s\\t%s\\tfail\\n" "${WS:-}" "$*" >>"$BASELINE_MAP"; echo "FAIL: $*";')
        matrix.write_text(code)
        for p in ("home", "tmp", "tools", "config", "state", "data", "cache", "runtime", "tmux"):
            (root / p).mkdir(mode=0o700)
        jq = shutil.which("jq")
        if not jq:
            raise SystemExit("jq is required for the owned Bash fixtures")
        (root / "tools/jq").symlink_to(jq)
        bash = shutil.which("bash")
        if not bash:
            raise SystemExit("Bash is required for the owned Bash fixtures")
        (root / "tools/bash").symlink_to(bash)
        env = {"HOME": str(root / "home"), "PATH": f"{root}/tools:/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR": str(root / "tmp"), "TMUX_TMPDIR": str(root / "tmux"), "BASELINE_MAP": str(root / "assertions.tsv"), "TERM": "dumb", "LC_ALL": "C"}
        env.update({f"XDG_{k}_HOME": str(root / p) for k, p in (("CONFIG", "config"), ("STATE", "state"), ("DATA", "data"), ("CACHE", "cache"))})
        env["XDG_RUNTIME_DIR"] = str(root / "runtime")
        result = subprocess.run([bash, str(matrix)], env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=300)
        log = result.stdout.decode()
        def normalize(text):
            text = text.replace(str(root.resolve()), "<fixture>")
            text = text.replace(str(root), "<fixture>")
            text = re.sub(r"volley-matrix\.[A-Za-z0-9]+", "workspace", text)
            text = re.sub(r"\b[0-9a-f]{8}-[0-9a-f-]{27}\b", "<session-id>", text)
            text = re.sub(r"volley-[0-9a-f]{8}", "volley-<run>", text)
            text = re.sub(r"(?<=/prompts/)[0-9a-f]{8}-", "<run>-", text)
            text = re.sub(r"\[volley \d\d:\d\d:\d\d\]", "[volley <time>]", text)
            text = re.sub(r"Started: \d{4}-\d\d-\d\dT[^\n]+", "Started: <time>", text)
            return text
        assertions = []
        workspaces = []
        for row in (root / "assertions.tsv").read_text().splitlines():
            workspace, label, observed = row.split("\t")
            if workspace not in workspaces:
                workspaces.append(workspace)
            cases, changes = target(label)
            assertions.append({"id": f"BASE-{len(assertions)+1:03d}", "scenario": workspaces.index(workspace), "assertion": label, "observed": observed, "target_cases": cases, "corrections": changes})
        captures = []
        for index, workspace in enumerate(workspaces):
            ws = Path(workspace)
            artifacts = []
            for f in sorted(ws.rglob("*")):
                if not f.is_file() or f.is_symlink():
                    continue
                path = f.relative_to(ws).as_posix()
                if path.startswith("home/") or path.startswith("skill-src/"):
                    continue
                raw = f.read_bytes()
                artifacts.append({"path": normalize(path), "sha256": sha(raw), "content": normalize(raw.decode(errors="replace"))})
            captures.append({"scenario": index, "artifacts": artifacts})
        counts = {"planner_calls": 0, "critic_calls": 0, "gashki_send_calls": 0, "live_vendor_launches": 0, "live_pastes": 0}
        for workspace in workspaces:
            ws = Path(workspace)
            for role in ("planner", "critic"):
                p = ws / f"mock-state/{role}-calls"
                if p.exists():
                    counts[f"{role}_calls"] += int(p.read_text())
            p = ws / "mock-state/gk/calls"
            if p.exists():
                counts["gashki_send_calls"] += sum(line.startswith("send ") for line in p.read_text().splitlines())
        record = {"source_commit": PIN, "source_sha256": hashes, "case_ids": ["A-BASE-01", "A-BASE-02"], "tier": "A", "fixtures": ["F-SRC", "F-DIRECT", "F-GK-CANNED"], "target_os": os.uname().sysname + "/" + os.uname().machine, "bash": subprocess.check_output([bash, "--version"], text=True).splitlines()[0], "settings": "Each argv/prompt and Gashki call is saved in the scenario artifacts. All executable paths are owned stubs; HOME/XDG/temp/tmux roots are isolated.", "historical_assertions": 588, "numeric_target": False, "matrix_exit": result.returncode, "process_counts": counts, "assertions": assertions, "captures": captures}
        payload = json.dumps(record, sort_keys=True, separators=(",", ":")).encode()
        (DEST / "bash-capture.json.gz").write_bytes(gzip.compress(payload, mtime=0))
        manifest = {k: v for k, v in record.items() if k != "captures"}
        manifest["capture_sha256"] = sha((DEST / "bash-capture.json.gz").read_bytes())
        manifest["normalizations"] = ["owned fixture paths", "random session/run IDs", "log and provenance times"]
        (DEST / "baseline.json").write_text(json.dumps(manifest, indent=2) + "\n")
        print(log.splitlines()[-1])
        print(json.dumps({"scenarios": len(captures), "assertions": len(assertions), "counts": counts, "capture_sha256": manifest["capture_sha256"]}))
        if result.returncode:
            print("\n".join(line for line in log.splitlines() if line.startswith("FAIL:")))
            raise SystemExit(result.returncode)


if __name__ == "__main__":
    main()
