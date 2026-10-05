#!/usr/bin/env python3
"""Build the pinned, isolated tier-B fixture. This is test tooling only."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile

PIN = "8eaecc9b31c965bab6c63a6a5562ebf38c43e363"
BASE = "https://github.com/Ozhiaki/gashki/schema/"


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--source", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--target", choices=["darwin/arm64", "linux/arm64", "darwin/amd64", "linux/amd64"], required=True)
    p.add_argument("--engine-work", type=Path, help="Owned review-loop stub extension")
    a = p.parse_args()
    out = a.output.resolve()
    out.mkdir(mode=0o700)  # Refuse reuse of an existing artifact directory.
    source = out / "source"
    source.mkdir(mode=0o700)
    archive = subprocess.check_output(["git", "-C", str(a.source), "archive", PIN])
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        tar.extractall(source, filter="data")
    go = shutil.which("go")
    if not go:
        raise SystemExit("Go is required")
    cache = subprocess.check_output([go, "env", "GOCACHE"], text=True).strip()
    modcache = subprocess.check_output([go, "env", "GOMODCACHE"], text=True).strip()
    for name in ["home", "config", "state", "data", "cache", "runtime", "tmp", "tools"]:
        (out / name).mkdir(mode=0o700)
    env = {"PATH": str(out / "tools"), "HOME": str(out / "home"), "TMPDIR": str(out / "tmp"),
           "GOCACHE": cache, "GOMODCACHE": modcache, "GOTOOLCHAIN": "local", "CGO_ENABLED": "0",
           "GOOS": a.target.split("/")[0], "GOARCH": a.target.split("/")[1],
           "XDG_CONFIG_HOME": str(out / "config"), "XDG_STATE_HOME": str(out / "state"),
           "XDG_DATA_HOME": str(out / "data"), "XDG_CACHE_HOME": str(out / "cache"),
           "XDG_RUNTIME_DIR": str(out / "runtime")}
    records = {}
    for name, tags in [("gashki-release", []), ("gashki-fault", ["gashkitest"])]:
        argv = [go, "build", "-trimpath", "-buildvcs=false"]
        if tags:
            argv += ["-tags=" + ",".join(tags)]
        argv += ["-o", str(out / name), "./cmd/gashki"]
        with (out / (name + ".build.log")).open("wb") as log:
            subprocess.run(argv, cwd=source, env=env, stdout=log, stderr=log, check=True)
        info = subprocess.check_output([go, "version", "-m", str(out / name)], text=True, env=env)
        (out / (name + ".build-info.txt")).write_text(info)
        records[name] = {"sha256": sha(out / name), "tags": tags, "build_info_sha256": sha(out / (name + ".build-info.txt"))}
    text = (source / "internal/app/spawn_test.go").read_text()
    stub = text.split("const stubAgent = `", 1)[1].split("`", 1)[0]
    (out / "upstream-agent-stub").write_text(stub)
    seam = '  return unless $hook{$ev};\n'
    if stub.count(seam) != 1:
        raise SystemExit("Pinned hook instrumentation seam changed")
    stub = stub.replace(seam, seam + '  put("$agent.hooks", "$ev\\n");\n')
    if a.engine_work:
        seam = "      fire('Stop');"
        if stub.count(seam) != 1:
            raise SystemExit("Pinned turn completion seam changed")
        work = a.engine_work.read_text()
        stub = stub.replace(seam, "      engine_work($p);\n" + seam) + "\n" + work
    (out / "agent-stub").write_text(stub)
    (out / "agent-stub").chmod(0o700)
    caps = json.loads((source / "internal/contract/capabilities.json").read_text())
    (out / "capabilities.json").write_text(json.dumps(caps, ensure_ascii=False))
    src = json.loads((source / "internal/contract/schemas.json").read_text())
    meta = json.loads((source / "internal/envelope/meta.schema.json").read_text())
    schemas = {}
    for name, value in src["verbs"].items():
        schema = {"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": BASE + "data/" + name.replace(" ", "-") + ".json", "title": "gashki " + name + " data", **value}
        if '"#/$defs/' in json.dumps(value):
            schema["$defs"] = src["defs"]
        schemas[name] = schema
    schema_data = {"envelope_schema": src["envelope"], "schemas": schemas, "streams": {}, "definitions": {"meta": meta, **src["definitions"]}}
    (out / "schemas.json").write_text(json.dumps(schema_data, ensure_ascii=False))
    files = {name: sha(out / name) for name in ["agent-stub", "upstream-agent-stub", "capabilities.json", "schemas.json"]}
    record = {"fixture": "F-GK-REAL", "source_commit": PIN, "source_archive_sha256": hashlib.sha256(archive).hexdigest(), "target": a.target, "binaries": records, "files": files}
    (out / "fixture.json").write_text(json.dumps(record, indent=2) + "\n")
    print(json.dumps(record))


if __name__ == "__main__":
    main()
