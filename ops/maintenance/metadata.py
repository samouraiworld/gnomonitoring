#!/usr/bin/env python3
"""Fixed read-only package/runtime and backup lifecycle metadata, no secrets."""
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import subprocess


PACKAGES = ("openssh-server", "openssl", "libssl3", "libssl3t64", "libc6", "curl", "docker-ce", "docker-ce-cli",
            "containerd.io", "age", "unattended-upgrades")
COMMANDS = {
    "kernel": ("uname", "-r"),
    "docker_server": ("docker", "version", "--format", "{{.Server.Version}}"),
    "compose": ("docker", "compose", "version", "--short"),
    "packages": ("dpkg-query", "-W", "-f=${binary:Package}\t${Version}\n", *PACKAGES),
    "lifecycle": ("aws", "--endpoint-url", "https://s3.fr-par.scw.cloud", "--region", "fr-par",
                  "s3api", "get-bucket-lifecycle-configuration", "--bucket", "gnomonitoring", "--output", "json"),
}


def run_command(command):
    if tuple(command) not in COMMANDS.values():
        raise ValueError("unsupported_read_command")
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=20, check=False)
        if len(result.stdout) > 1024 * 1024:
            return 1, ""
        return result.returncode, result.stdout
    except (OSError, subprocess.TimeoutExpired):
        return 1, ""


def lifecycle_summary(data):
    rules = data.get("Rules")
    if not isinstance(rules, list):
        return {"status": "invalid_response"}
    result = {"status": "ok", "rule_count": len(rules), "postgres_rules": [],
              "unrelated_rule_count": 0, "unknown_filter_count": 0}
    for rule in rules:
        if not isinstance(rule, dict):
            result["unknown_filter_count"] += 1
            continue
        selected = rule.get("Filter", {"Prefix": rule.get("Prefix", "")})
        if not isinstance(selected, dict):
            result["unknown_filter_count"] += 1
            continue
        condition = selected.get("And", selected)
        if not isinstance(condition, dict):
            result["unknown_filter_count"] += 1
            continue
        prefix = condition.get("Prefix", "")
        if not isinstance(prefix, str):
            result["unknown_filter_count"] += 1
            continue
        if "postgres/".startswith(prefix):
            scope = "entire_prefix"
        elif prefix.startswith("postgres/"):
            scope = "partial_prefix"
        else:
            result["unrelated_rule_count"] += 1
            continue
        conditional = bool(set(condition) - {"Prefix"}) or bool(set(selected) - {"Prefix", "And"})
        expiration = rule.get("Expiration") or {}
        days = expiration.get("Days") if isinstance(expiration, dict) else None
        date = expiration.get("Date") if isinstance(expiration, dict) else None
        result["postgres_rules"].append({"enabled": rule.get("Status") == "Enabled",
            "scope": scope, "additional_conditions": conditional,
            "expiration_days": days if isinstance(days, int) and not isinstance(days, bool) else None,
            "fixed_expiration_date_present": isinstance(date, str),
            "noncurrent_expiration_present": "NoncurrentVersionExpiration" in rule})
    return result


def collect(runner=run_command):
    output = {"observed_at": datetime.now(timezone.utc).isoformat(), "read_only": True,
              "security_advisory_freshness": "not_evaluated", "installed_packages": {}}
    for key in ("kernel", "docker_server", "compose"):
        code, value = runner(COMMANDS[key])
        value = value.strip()
        output[key] = value if code == 0 and re.fullmatch(r"[A-Za-z0-9.+:~_/-]{1,160}", value) else "unavailable"
    code, packages = runner(COMMANDS["packages"])
    if code in (0, 1):
        for line in packages.splitlines():
            fields = line.split("\t")
            if len(fields) == 2:
                name, version = fields
                name = name.split(":", 1)[0]
                if name in PACKAGES and re.fullmatch(r"[A-Za-z0-9.+:~_-]{1,160}", version):
                    output["installed_packages"][name] = version
    output["reboot_required_flag"] = Path("/var/run/reboot-required").exists()
    try:
        for line in Path("/proc/stat").read_text().splitlines():
            if re.fullmatch(r"btime [0-9]+", line):
                output["boot_utc"] = datetime.fromtimestamp(int(line.split()[1]), timezone.utc).isoformat()
                break
    except OSError:
        pass
    code, raw = runner(COMMANDS["lifecycle"])
    try:
        output["backup_lifecycle"] = lifecycle_summary(json.loads(raw)) if code == 0 else {"status": "unavailable"}
    except (ValueError, TypeError, AttributeError):
        output["backup_lifecycle"] = {"status": "invalid_response"}
    return output


if __name__ == "__main__":
    print(json.dumps(collect(), sort_keys=True))
