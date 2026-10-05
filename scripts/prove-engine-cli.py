#!/usr/bin/env python3
"""Exercise the native run slice with owned agent processes and isolated roots."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import pty
import signal
import subprocess
import time


def digest(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def setup(base, name, agent_binary, question=False, hold=False):
    root = base / name
    root.mkdir(mode=0o700)
    for part in ("ws", "home", "tmp", "config", "state", "data", "cache", "runtime", "tools", "capture"):
        (root / part).mkdir(mode=0o700)
    (root / "ws/SPEC.md").write_text("# Owned seed\nReview this exact file.\n")
    plan = {"question_purpose": "draft" if question else "", "hold": hold}
    (root / "plan.json").write_bytes(canonical(plan))
    for provider in ("claude", "codex"):
        # sh quoting is explicit; neither executable path is expanded by a shell.
        quoted = "'" + str(agent_binary).replace("'", "'\\''") + "'"
        wrapper = f'#!/bin/sh\nexec {quoted} -test.run=^TestEngineAgentChild$ -- --engine-child {provider} "$@"\n'
        path = root / "tools" / provider
        path.write_text(wrapper)
        path.chmod(0o700)
    settings = root / "settings.toml"
    settings.write_text("closing_pass = false\n" + "\n".join(
        f"{provider}_bin = {json.dumps(str(root / 'tools' / provider))}" for provider in ("claude", "codex")) + "\n")
    env = {"HOME": str(root / "home"), "TMPDIR": str(root / "tmp"), "PATH": str(root / "tools"),
           "XDG_CONFIG_HOME": str(root / "config"), "XDG_STATE_HOME": str(root / "state"),
           "XDG_DATA_HOME": str(root / "data"), "XDG_CACHE_HOME": str(root / "cache"),
           "XDG_RUNTIME_DIR": str(root / "runtime"), "F_ENGINE_CAPTURE": str(root / "capture"),
           "F_ENGINE_PLAN": str(root / "plan.json"), "GORACE": "atexit_sleep_ms=0"}
    return root, settings, env


def launches(root):
    path = root / "capture/launches.txt"
    return path.read_text().splitlines() if path.exists() else []


def invoke(binary, root, settings, env, args, stdin=None, expected=0, code=None):
    argv = [str(binary), "--config", str(settings), "--json", *args]
    result = subprocess.run(argv, cwd=root, env=env, input=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=40)
    envelope = json.loads(result.stdout)
    assert len(result.stdout.splitlines()) == 1, result.stdout
    assert set(envelope) == {"ok", "tool_version", "data", "meta", "warnings", "commands", "errors"}, envelope
    assert envelope["meta"]["data_hash"] == "sha256:" + digest(canonical(envelope["data"])), envelope
    assert result.returncode == expected, (result.returncode, expected, result.stdout, result.stderr)
    if code:
        assert envelope["errors"][0]["code"] == code, envelope
    else:
        assert envelope["ok"] is True and not envelope["errors"], envelope
    recorded_argv = [a.decode("utf-8", errors="backslashreplace") if isinstance(a, bytes) else a for a in argv]
    return {"argv": recorded_argv, "argv_bytes": [os.fsencode(a).hex() for a in argv], "stdin_sha256": digest(stdin) if stdin is not None else None,
            "exit": result.returncode, "stdout": envelope, "stderr": result.stderr.decode()}, envelope


def evidence(root, name, records, binary, agent_binary):
    hashes = {}
    for path in root.rglob("*"):
        if path.is_file() and not path.is_symlink():
            data = path.read_bytes()
            hashes[str(path.relative_to(root))] = {"sha256": digest(data), "bytes": len(data)}
    value = {"case": name, "tier": "A", "fixture": "F-DIRECT/installed CLI", "target_os": platform.system(),
             "binary_sha256": digest(binary.read_bytes()), "agent_binary_sha256": digest(agent_binary.read_bytes()),
             "settings": (root / "settings.toml").read_text(), "observations": records,
             "owned_agent_launches": len(launches(root)), "calls": launches(root), "artifact_hashes": hashes, "passed": True}
    (root / "evidence.json").write_bytes(canonical(value))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--agent-binary", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    options = parser.parse_args()
    binary, agent_binary, base = options.binary.resolve(), options.agent_binary.resolve(), options.evidence.resolve()
    base.mkdir(mode=0o700, parents=True, exist_ok=True)
    grammar = [
        ("default", [], 0, None), ("bool-equals", ["--persistent=true"], 0, None),
        ("bool-space", ["--persistent", "false"], 0, None), ("empty-model", ["--claude-model="], 0, None),
        ("array", ["--claude-critic-tools", '["Read","Grep"]'], 0, None),
        ("missing-value", ["--max-rounds"], 1, "INVALID_INPUT"), ("empty-value", ["--max-rounds="], 1, "INVALID_INPUT"),
        ("invalid-number", ["--max-rounds=2.5"], 1, "INVALID_INPUT"), ("zero-cap", ["--max-rounds=0"], 1, "INVALID_INPUT"),
        ("repeated", ["--max-rounds=2", "--max-rounds=3"], 1, "INVALID_INPUT"),
        ("bad-enum", ["--planner=cloude"], 1, "INVALID_INPUT"), ("bad-bool", ["--persistent=yes"], 1, "INVALID_INPUT"),
        ("bare-bool", ["--persistent"], 1, "INVALID_INPUT"), ("bad-array", ["--claude-critic-tools=[1]"], 1, "INVALID_INPUT"),
        ("array-trailing", ["--claude-critic-tools", '["Read"] []'], 1, "INVALID_INPUT"),
        ("selector-value", ["--wait=true"], 1, "INVALID_INPUT"), ("json-value", ["--json=true"], 1, "INVALID_INPUT"),
        ("json-repeat", ["--json"], 1, "INVALID_INPUT"), ("unknown", ["--bogus"], 1, "UNKNOWN_FLAG"),
        ("interactive-json", ["--wait", "--interactive"], 1, "INVALID_INPUT"),
        ("interactive-without-wait", ["--interactive"], 1, "INVALID_INPUT"),
        ("brief-seed", ["--brief=x", "--seed=y"], 1, "INVALID_INPUT"),
        ("seed-brief", ["--seed=y", "--brief=x"], 1, "INVALID_INPUT"),
        ("literal-extra", ["--", "--json"], 1, "INVALID_INPUT"),
        ("array-null", ["--claude-critic-tools=null"], 1, "INVALID_INPUT"),
        ("invalid-utf8-attached", [b"--idempotency-key=\xff"], 1, "INVALID_INPUT"),
        ("invalid-utf8-separated", ["--idempotency-key", b"\xff"], 1, "INVALID_INPUT"),
        ("undeclared-negation", ["--no-persistent"], 1, "UNKNOWN_FLAG"),
    ]
    for name, extra, exit_code, code in grammar:
        root, settings, env = setup(base, "grammar-" + name, agent_binary)
        record, _ = invoke(binary, root, settings, env, ["run", str(root / "ws"), *extra], expected=exit_code, code=code)
        assert len(launches(root)) == (1 if exit_code == 0 else 0), launches(root)
        evidence(root, "grammar-" + name, [record], binary, agent_binary)
        print("PASS", name, flush=True)
    for name in ("source-match", "source-new", "source-different", "source-missing", "source-empty", "source-invalid", "basis-missing", "basis-empty"):
        root, settings, env = setup(base, "seed-" + name, agent_binary)
        source = root / "seed.md"
        source.write_bytes((root / "ws/SPEC.md").read_bytes())
        expected, code = 0, None
        args = ["run", str(root / "ws"), "--seed", str(source), "--idempotency-key", "seed-key"]
        if name == "source-new": (root / "ws/SPEC.md").unlink()
        if name == "source-different": source.write_text("different\n"); expected, code = 5, "CONFIG_CONFLICT"
        if name == "source-missing": source.unlink(); expected, code = 1, "NOT_FOUND"
        if name == "source-empty": source.write_text("\n"); expected, code = 1, "INVALID_INPUT"
        if name == "source-invalid": source.write_bytes(b"\xff"); expected, code = 1, "INVALID_INPUT"
        if name in ("basis-missing", "basis-empty"):
            args = ["run", str(root / "ws")]; expected, code = 1, "INVALID_INPUT"
            if name == "basis-missing": (root / "ws/SPEC.md").unlink()
            else: (root / "ws/SPEC.md").write_text(" \n")
        record, _ = invoke(binary, root, settings, env, args, expected=expected, code=code)
        assert len(launches(root)) == (1 if expected == 0 else 0)
        if expected != 0: assert not (root / "ws/state/manifest.json").exists()
        evidence(root, name, [record], binary, agent_binary)
        print("PASS", name, flush=True)
    for name in ("answer", "answer-repeat", "stale", "null", "duplicate-field", "unknown-field", "oversized", "file", "skip-guard", "skip", "steer", "both-sources"):
        root, settings, env = setup(base, "human-" + name, agent_binary, question=True)
        (root / "ws/SPEC.md").unlink()
        (root / "ws/BRIEF.md").write_text("# Owned brief\nDraft this file.\n")
        first, response = invoke(binary, root, settings, env, ["run", str(root / "ws")], expected=8, code="ANSWER_REQUIRED")
        question = response["data"]["question_id"]
        payload = canonical({"text": "  exact\r\nanswer\n", "idempotency_key": "owned-key"})
        args = ["human", "answer", str(root / "ws"), "--question-id", question, "--from-stdin"]
        expected, code = 0, None
        if name == "stale": args[4] = "aaaaaaaaaaaaaaaaaaaaaaaaaa"; expected, code = 8, "ANSWER_CONFLICT"
        if name == "null": payload = b'{"text":null}'; expected, code = 1, "INVALID_INPUT"
        if name == "duplicate-field": payload = b'{"text":"a","text":"b"}'; expected, code = 1, "INVALID_INPUT"
        if name == "unknown-field": payload = b'{"text":"a","other":1}'; expected, code = 1, "INVALID_INPUT"
        if name == "oversized": payload = canonical({"text": "x" * (1024 * 1024 + 1)}); expected, code = 1, "INVALID_INPUT"
        if name == "file":
            source = root / "answer.txt"; source.write_bytes(b"  exact\r\nanswer\n")
            args[-1:] = ["--file", str(source)]; payload = None
        if name in ("skip-guard", "skip"):
            args = ["human", "skip", str(root / "ws"), "--question-id", question]; payload = None
            if name == "skip": args += ["--yes"]
            else: expected, code = 2, "ACK_REQUIRED"
        if name == "steer": args = ["human", "steer", str(root / "ws"), "--from-stdin"]
        if name == "both-sources": args += ["--file", "answer.txt"]; expected, code = 1, "INVALID_INPUT"
        record, response = invoke(binary, root, settings, env, args, payload, expected, code)
        records = [first, record]
        if name == "answer-repeat":
            repeated, again = invoke(binary, root, settings, env, args, payload)
            assert again["data"]["receipt_id"] == response["data"]["receipt_id"] and again["data"]["duplicate"] is True
            records.append(repeated)
        assert len(launches(root)) == 1, "submission launched an agent"
        if name in ("answer", "answer-repeat", "file", "skip"):
            resumed, response = invoke(binary, root, settings, env, ["runs", "resume", str(root / "ws")])
            records.append(resumed); assert response["data"]["status"] == "approved" and len(launches(root)) == 3
        if name == "steer":
            resumed, _ = invoke(binary, root, settings, env, ["runs", "resume", str(root / "ws")], expected=8, code="ANSWER_REQUIRED")
            records.append(resumed); assert len(launches(root)) == 1
        evidence(root, "human-" + name, records, binary, agent_binary)
        print("PASS", name, flush=True)
    for active in (False, True):
        for sig, expected, code in ((signal.SIGINT, 130, "CONTROLLER_INTERRUPTED"), (signal.SIGTERM, 143, "CONTROLLER_STOPPED")):
            name = f"signal-{'active' if active else 'waiting'}-{sig.name}"
            root, settings, env = setup(base, name, agent_binary, question=not active, hold=active)
            if not active:
                (root / "ws/SPEC.md").unlink(); (root / "ws/BRIEF.md").write_text("# Owned brief\nDraft this file.\n")
            argv = [str(binary), "--config", str(settings), "--json", "run", str(root / "ws"), "--wait"]
            child = subprocess.Popen(argv, cwd=root, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            deadline = time.monotonic() + 20
            reached = False
            while time.monotonic() < deadline:
                path = root / "ws/state/manifest.json"
                if path.exists():
                    state = json.loads(path.read_bytes())
                    if (active and launches(root)) or (not active and state["status"] == "awaiting_answer"):
                        reached = True; break
                if child.poll() is not None: break
                time.sleep(0.05)
            assert reached, name
            child.send_signal(sig)
            stdout, stderr = child.communicate(timeout=15)
            response = json.loads(stdout)
            assert child.returncode == expected and response["errors"][0]["code"] == code, (name, child.returncode, stdout, stderr)
            state = json.loads((root / "ws/state/manifest.json").read_bytes())
            assert state["status"] == "handover" and len(launches(root)) == 1
            record = {"argv": argv, "signal": sig.name, "controller_pid": child.pid, "exit": child.returncode, "stdout": response, "stderr": stderr.decode()}
            evidence(root, name, [record], binary, agent_binary)
            print("PASS", name, flush=True)
    for status in ("approved", "awaiting_answer", "impasse"):
        for key in ("", "owned-key"):
            name = f"attachment-{status}-{'keyed' if key else 'natural'}"
            root, settings, env = setup(base, name, agent_binary, question=status == "awaiting_answer")
            args = ["run", str(root / "ws")]
            expected, code = 0, None
            if key: args += ["--idempotency-key", key]
            if status == "awaiting_answer":
                (root / "ws/SPEC.md").unlink(); (root / "ws/BRIEF.md").write_text("# Owned brief\nDraft this file.\n")
                expected, code = 8, "ANSWER_REQUIRED"
            if status == "impasse":
                (root / "plan.json").write_bytes(canonical({"critiques": ["Review\nVERDICT: REVISE\n"]}))
                args += ["--max-rounds", "1"]
                expected, code = 7, "REVIEW_IMPASSE"
            first, response = invoke(binary, root, settings, env, args, expected=expected, code=code)
            count = len(launches(root)); run_id = response["data"]["run_id"]
            attached, response = invoke(binary, root, settings, env, args, expected=expected, code=code)
            assert response["data"]["status"] == status and response["data"]["run_id"] == run_id and len(launches(root)) == count
            evidence(root, name, [first, attached], binary, agent_binary)
            print("PASS", name, flush=True)
    for mode in ("answer", "eof"):
        name = "terminal-" + mode
        root, settings, env = setup(base, name, agent_binary, question=True)
        (root / "ws/SPEC.md").unlink(); (root / "ws/BRIEF.md").write_text("# Owned brief\nDraft this file.\n")
        master, slave = pty.openpty()
        argv = [str(binary), "--config", str(settings), "run", str(root / "ws"), "--wait", "--interactive"]
        child = subprocess.Popen(argv, cwd=root, env=env, stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        os.close(slave)
        try:
            deadline = time.monotonic() + 20
            reached = False
            while time.monotonic() < deadline:
                path = root / "ws/state/manifest.json"
                if path.exists() and json.loads(path.read_bytes())["status"] == "awaiting_answer":
                    reached = True; break
                if child.poll() is not None: break
                time.sleep(0.05)
            assert reached, name
            os.write(master, b"exact terminal answer\n\n" if mode == "answer" else b"\x04")
            stdout, stderr = child.communicate(timeout=20)
            state = json.loads((root / "ws/state/manifest.json").read_bytes())
            if mode == "answer":
                assert child.returncode == 0 and json.loads(stdout)["status"] == "approved" and len(launches(root)) == 3, (stdout, stderr)
            else:
                assert child.returncode == 8 and b"ANSWER_REQUIRED" in stdout and state["status"] == "awaiting_answer" and len(launches(root)) == 1, (stdout, stderr)
                assert state["question"]["answered"] is False
            record = {"argv": argv, "terminal_input": mode, "exit": child.returncode, "stdout": stdout.decode(), "stderr": stderr.decode()}
            evidence(root, name, [record], binary, agent_binary)
            print("PASS", name, flush=True)
        finally:
            os.close(master)
            if child.poll() is None:
                child.kill(); child.wait()
    for key in ("", "owned-key"):
        name = f"attachment-frozen-defaults-{'keyed' if key else 'natural'}"
        root, settings, env = setup(base, name, agent_binary)
        initial = ["run", str(root / "ws"), "--claude-model", "selected-model", "--persistent", "true", "--max-rounds", "1"]
        attached = ["run", str(root / "ws")]
        if key: initial += ["--idempotency-key", key]; attached += ["--idempotency-key", key]
        first, response = invoke(binary, root, settings, env, initial)
        run_id = response["data"]["run_id"]
        record, response = invoke(binary, root, settings, env, attached)
        assert response["data"]["run_id"] == run_id and response["data"]["status"] == "approved" and len(launches(root)) == 1
        changed, _ = invoke(binary, root, settings, env, attached + ["--claude-model", "different"], expected=5, code="IDEMPOTENCY_CONFLICT" if key else "CONFIG_CONFLICT")
        assert len(launches(root)) == 1
        evidence(root, name, [first, record, changed], binary, agent_binary)
        print("PASS", name, flush=True)
    print("PASS installed CLI: 62 cases; 4 actual controller signals; 2 owned TTY cases", flush=True)


if __name__ == "__main__":
    main()
