#!/usr/bin/env python3
"""Check the declaration boundary before parser and handler implementation."""
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent / "internal/contract"


def check(registry):
    assert registry["contract_version"] == "1"
    assert len(registry["diagnosis_order"]) == 8
    assert len(set(registry["diagnosis_order"])) == 8
    assert registry["legacy_exit_mapping"] == {"0": 0, "7": 2, "130": 130, "143": 143, "other": 1}
    assert len({c["code"] for c in registry["error_codes"]}) == len(registry["error_codes"])
    probes = {p["id"]: p for p in registry["registry_probes"]}
    assert set(probes) == {f"A-CONTRACT-R{i:02}" for i in range(1, 44)}
    for code in registry["error_codes"]:
        assert code["arrays"] == ["errors"]
        assert code["exit"] == probes[code["fixture"]]["exit"]
        assert code["code"] == probes[code["fixture"]]["code"]
        assert code["precondition"]
        assert code["retryable"] == registry["exit_codes"][str(code["exit"])]["retryable"]
    settings = {s["key"]: s for s in registry["settings"]}
    assert len(settings) == len(registry["settings"])
    assert [s["order"] for s in registry["settings"]] == list(range(len(settings)))
    for path, command in registry["commands"].items():
        assert command["path"] == path
        assert command["parser_path"] and command["handler_path"]
        assert command["advertisement"] == "registered_handlers_only"
        assert "frames" not in command["output_modes"]
        assert "follow" not in path and "import" not in path
        names = set()
        for f in registry["global_flags"] + command["flags"]:
            assert not (names & {f["name"], *f["aliases"]}), (path, f["name"])
            names.update([f["name"], *f["aliases"]])
            assert f["arity"] in [0, 1]
            assert f["type"] in ["string", "integer", "boolean", "array", "path", "duration", "executable"]
            assert f["repeatable"] is False
            assert f["parser_path"]
            if "setting" in f:
                assert f["setting"] in settings
                assert f["arity"] == 1
                assert f["default"] == settings[f["setting"]]["default"]
        assert (ROOT / "schemas" / (command["schema"] + ".json")).is_file()
    for env in registry["environment_reads"]:
        assert env["purpose"] and env["scope"]
        if env["name"] in registry["retired_setting_variables"]:
            assert env["scope"] == "doctor"
    for p in (ROOT / "schemas").glob("*.json"):
        schema = json.loads(p.read_text())
        assert schema["$schema"] == "https://json-schema.org/draft/2020-12/schema"
        def walk(v):
            if isinstance(v, dict):
                if "$ref" in v and not v["$ref"].startswith("#"):
                    assert (p.parent / v["$ref"]).is_file(), (p, v["$ref"])
                if v.get("type") == "object" and "properties" in v:
                    assert v.get("additionalProperties") is False, p
                    assert set(v.get("required", [])) <= set(v["properties"]), p
                for child in v.values():
                    walk(child)
            elif isinstance(v, list):
                for child in v:
                    walk(child)
        walk(schema)


def main():
    registry = json.loads((ROOT / "registry.json").read_text())
    check(registry)
    # A missing parser path, duplicate flag, or extra error exit must fail.
    for mutate in (
        lambda r: r["commands"]["run"].update(parser_path=""),
        lambda r: r["commands"]["run"]["flags"].append(r["global_flags"][0]),
        lambda r: r["error_codes"][0].update(exit=99),
    ):
        bad = json.loads(json.dumps(registry))
        mutate(bad)
        try:
            check(bad)
        except (AssertionError, KeyError):
            continue
        raise AssertionError("Malformed declaration was accepted")
    print(json.dumps({"case": "A-CLI-01", "fixture": "F-PURE", "tier": "A", "observed": "declarations and three negative mutations checked", "commands": len(registry["commands"]), "settings": len(registry["settings"]), "codes": len(registry["error_codes"]), "probes": len(registry["registry_probes"]), "registry_sha256": hashlib.sha256((ROOT / "registry.json").read_bytes()).hexdigest(), "agent_processes": 0}))


if __name__ == "__main__":
    main()
