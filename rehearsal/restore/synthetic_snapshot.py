"""Independent equality oracle for frozen, invented PostgreSQL fixtures only.

This receipt is an integrity/provenance record, not a signed attestation and never
claims equality with a production snapshot. No runner SQL or baseline is reused.
"""

import hashlib
import json
import os
from pathlib import Path
import stat

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
RECEIPT_NAME = "synthetic-source-receipt.json"
PG_IMAGE = "postgres:16@sha256:9e2cf6287f25c3151f4c9421ccce2b17f3e60061c111a29165e0d2cdd9ff0a97"
MAX_BYTES = 16 * 1024 * 1024
OWNER_MAP = {"fixture_owner": "rehearsal_owner", "fixture_admin": "rehearsal_admin"}
PREFIX = "SET search_path=pg_catalog; SET TimeZone='UTC'; SET DateStyle='ISO, YMD'; SET extra_float_digits=3; SET bytea_output='hex'; SET standard_conforming_strings=on; "


class SnapshotError(Exception):
    error_class = "synthetic_source_receipt_invalid"


class SnapshotMismatch(SnapshotError):
    error_class = "synthetic_snapshot_mismatch"

    def __init__(self, differences):
        # Report field paths only: no row, SQL, role or endpoint payload.
        self.differences = differences
        super().__init__(self.error_class)


def canonical_bytes(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=True, separators=(",", ":"), allow_nan=False).encode("ascii")


def sha256(value):
    return hashlib.sha256(value).hexdigest()


def quote_identifier(value):
    return '"' + value.replace('"', '""') + '"'


def _query(sql, query):
    raw = sql(PREFIX + query)
    if isinstance(raw, bytes):
        raw = raw.decode("utf-8")
    if len(raw.encode("utf-8")) > MAX_BYTES:
        raise SnapshotError("synthetic_snapshot_budget_exceeded")
    return json.loads(raw)


def _normalize_ownership(value):
    if isinstance(value, list):
        return [_normalize_ownership(item) for item in value]
    if isinstance(value, dict):
        normalized = {key: (OWNER_MAP.get(item, item) if key in {"owner", "grantor", "grantee"} and isinstance(item, str)
                           else _normalize_ownership(item)) for key, item in value.items()}
        # ACL row order depends on role OIDs, which are intentionally recreated.
        if isinstance(normalized.get("acl"), list):
            normalized["acl"] = sorted(normalized["acl"], key=canonical_bytes)
        return normalized
    return value


def content_fingerprints(rows, columns):
    """Multiset hashes of typed-column text values, globally and per chain.

    PostgreSQL renders each column as text under fixed session settings. SQL NULL
    is represented by JSON null, distinct from an empty string. Duplicate rows
    remain duplicate entries; sorted hashes remove physical row ordering only.
    """
    chain_index = columns.index("chain_id") if "chain_id" in columns else None
    groups = {}
    hashes = []
    for row in rows:
        if len(row) != len(columns) or any(item is not None and not isinstance(item, str) for item in row):
            raise SnapshotError("invalid_synthetic_row_encoding")
        digest = sha256(canonical_bytes(row))
        hashes.append(digest)
        key = ("global", None) if chain_index is None else (("null_chain_id", None) if row[chain_index] is None else ("chain", row[chain_index]))
        groups.setdefault(key, []).append(digest)
    def fingerprint(values):
        return {"rows": len(values), "payload_sha256": sha256(canonical_bytes(sorted(values)))}
    return {**fingerprint(hashes), "scopes": [{"scope": scope, "chain_id": chain, **fingerprint(values)}
             for (scope, chain), values in sorted(groups.items(), key=lambda item: canonical_bytes(item[0]))]}


def capture_snapshot(sql):
    """Capture independently from sql(query)->JSON text before any InitDB/write."""
    relations = _query(sql, """SELECT coalesce(json_agg(json_build_object(
'schema',n.nspname,'name',c.relname,'kind',c.relkind,'owner',pg_get_userbyid(c.relowner),
'persistence',c.relpersistence,'row_security',c.relrowsecurity,'force_row_security',c.relforcerowsecurity,
'options',c.reloptions,'acl',(SELECT coalesce(json_agg(json_build_object('grantor',pg_get_userbyid(a.grantor),'grantee',CASE WHEN a.grantee=0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END,'privilege',a.privilege_type,'grantable',a.is_grantable) ORDER BY a.grantee,a.privilege_type),'[]') FROM aclexplode(coalesce(c.relacl,acldefault(CASE WHEN c.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END,c.relowner))) a)
) ORDER BY n.nspname,c.relname),'[]') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname !~ '^pg_' AND n.nspname NOT IN ('information_schema','rehearsal_guard') AND c.relkind IN ('r','p','v','m','S')""")
    if len(relations) > 100:
        raise SnapshotError("synthetic_relation_budget_exceeded")
    result = {"format_version": 1, "relations": [], "types": [], "extensions": []}
    for relation in relations:
        qualified = quote_identifier(relation["schema"]) + "." + quote_identifier(relation["name"])
        literal = "'" + qualified.replace("'", "''") + "'::regclass"
        relation["columns"] = _query(sql, f"""SELECT coalesce(json_agg(json_build_object('position',a.attnum,'name',a.attname,'type',format_type(a.atttypid,a.atttypmod),'not_null',a.attnotnull,'identity',a.attidentity,'generated',a.attgenerated,'default',pg_get_expr(d.adbin,d.adrelid),'collation',CASE WHEN a.attcollation=0 THEN NULL ELSE a.attcollation::regcollation::text END) ORDER BY a.attnum),'[]') FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid={literal} AND a.attnum>0 AND NOT a.attisdropped""")
        if relation["kind"] == "S":
            relation["definition"] = _query(sql, f"SELECT row_to_json(s) FROM (SELECT seqtypid::regtype::text AS type,seqstart,seqincrement,seqmax,seqmin,seqcache,seqcycle FROM pg_sequence WHERE seqrelid={literal}) s")
            relation["state"] = _query(sql, f"SELECT row_to_json(s) FROM (SELECT last_value,is_called FROM {qualified}) s")
            relation["owned_by"] = _query(sql, f"SELECT coalesce(json_agg(json_build_object('schema',n.nspname,'table',c.relname,'column',a.attname,'dependency',d.deptype) ORDER BY n.nspname,c.relname,a.attname),'[]') FROM pg_depend d JOIN pg_class c ON c.oid=d.refobjid JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=d.refobjsubid WHERE d.classid='pg_class'::regclass AND d.objid={literal} AND d.refclassid='pg_class'::regclass AND d.deptype IN ('a','i')")
        else:
            relation["constraints"] = _query(sql, f"SELECT coalesce(json_agg(json_build_object('name',conname,'type',contype,'definition',pg_get_constraintdef(oid,true),'validated',convalidated,'deferrable',condeferrable,'deferred',condeferred) ORDER BY conname),'[]') FROM pg_constraint WHERE conrelid={literal}")
            relation["indexes"] = _query(sql, f"SELECT coalesce(json_agg(json_build_object('name',c.relname,'definition',pg_get_indexdef(i.indexrelid),'valid',i.indisvalid,'ready',i.indisready,'unique',i.indisunique,'primary',i.indisprimary) ORDER BY c.relname),'[]') FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indrelid={literal}")
            if relation["kind"] in {"v", "m"}:
                relation["definition"] = _query(sql, f"SELECT to_json(pg_get_viewdef({literal},true))")
            columns = [column["name"] for column in relation["columns"]]
            values = ",".join("t." + quote_identifier(name) + "::text" for name in columns)
            rows = _query(sql, f"SELECT coalesce(json_agg(json_build_array({values})),'[]') FROM {qualified} t")
            if len(rows) > 100000:
                raise SnapshotError("synthetic_row_budget_exceeded")
            relation["content"] = content_fingerprints(rows, columns)
        result["relations"].append(relation)
    result["types"] = _query(sql, """SELECT coalesce(json_agg(json_build_object('schema',n.nspname,'name',t.typname,'kind',t.typtype,'owner',pg_get_userbyid(t.typowner),'base',CASE WHEN t.typbasetype=0 THEN NULL ELSE format_type(t.typbasetype,t.typtypmod) END,'not_null',t.typnotnull,'default',t.typdefault,'enum',(SELECT json_agg(enumlabel ORDER BY enumsortorder) FROM pg_enum WHERE enumtypid=t.oid)) ORDER BY n.nspname,t.typname),'[]') FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname !~ '^pg_' AND n.nspname NOT IN ('information_schema','rehearsal_guard')""")
    result["extensions"] = _query(sql, "SELECT coalesce(json_agg(json_build_object('name',e.extname,'schema',n.nspname,'version',e.extversion,'owner',pg_get_userbyid(e.extowner),'relocatable',e.extrelocatable) ORDER BY e.extname),'[]') FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace")
    result["schemas"] = _query(sql, "SELECT coalesce(json_agg(json_build_object('name',n.nspname,'owner',pg_get_userbyid(n.nspowner),'acl',(SELECT coalesce(json_agg(json_build_object('grantor',pg_get_userbyid(a.grantor),'grantee',CASE WHEN a.grantee=0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END,'privilege',a.privilege_type,'grantable',a.is_grantable) ORDER BY a.grantee,a.privilege_type),'[]') FROM aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a)) ORDER BY n.nspname),'[]') FROM pg_namespace n WHERE n.nspname !~ '^pg_' AND n.nspname NOT IN ('information_schema','rehearsal_guard')")
    return _normalize_ownership(result)


# A descriptive alias for callers which prefer the acquisition terminology.
collect_snapshot = capture_snapshot


def compare_snapshots(source, target):
    """Compare every captured value; only acquisition applies explicit owner map."""
    differences = []
    def walk(left, right, path):
        if type(left) is not type(right):
            differences.append(path)
        elif isinstance(left, dict):
            for key in sorted(set(left) | set(right)):
                if key not in left or key not in right:
                    differences.append(path + "." + key)
                else:
                    walk(left[key], right[key], path + "." + key)
        elif isinstance(left, list):
            if len(left) != len(right):
                differences.append(path + ".length")
            for index, (first, second) in enumerate(zip(left, right)):
                walk(first, second, path + f"[{index}]")
        elif left != right:
            differences.append(path)
    walk(source, target, "snapshot")
    if differences:
        raise SnapshotMismatch(differences[:100])


def source_provenance(fixture_names):
    paths = ["rehearsal/restore/synthetic_bundle.py", "rehearsal/restore/synthetic_snapshot.py", "scripts/backup/prepare-postgres-backup.py"]
    if not fixture_names or any(name not in {"archive.sql", "reserved-marker.sql", "restore-error.sql", "restore-timeout.sql"} for name in fixture_names):
        raise SnapshotError("invalid_synthetic_fixture_provenance")
    paths.extend("rehearsal/restore/fixtures/" + name for name in fixture_names)
    return {path: sha256((ROOT / path).read_bytes()) for path in paths}


def receipt_path(bundle):
    return Path(bundle).parent.parent / RECEIPT_NAME


def write_source_receipt(bundle, snapshot, postgres_version, fixture_names):
    bundle = Path(bundle)
    receipt = {"format_version": 1, "kind": "gnomonitoring-synthetic-source-receipt", "synthetic_only": True,
               "source_frozen_before_dump": True, "source_snapshot_equality_verified": False,
               "source_postgres_version": postgres_version, "source_postgres_image": PG_IMAGE,
               "fixture_names": fixture_names, "provenance": source_provenance(fixture_names),
               "archive_sha256": sha256((bundle / "database.dump").read_bytes()),
               "bundle_files": {name: sha256((bundle / name).read_bytes()) for name in ("database.dump", "globals.sql", "toc.list", "manifest.json", "SHA256SUMS")},
               "snapshot": snapshot, "snapshot_sha256": sha256(canonical_bytes(snapshot))}
    receipt["receipt_sha256"] = sha256(canonical_bytes(receipt))
    path = receipt_path(bundle)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as output:
        output.write(canonical_bytes(receipt) + b"\n")
        output.flush()
        os.fsync(output.fileno())
    return path


def read_source_receipt(bundle, path=None):
    bundle = Path(bundle)
    path = Path(path) if path is not None else receipt_path(bundle)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > MAX_BYTES:
        raise SnapshotError("invalid_synthetic_receipt_file")
    receipt = json.loads(path.read_bytes())
    digest = receipt.pop("receipt_sha256", None)
    if sha256(canonical_bytes(receipt)) != digest:
        raise SnapshotError("synthetic_receipt_digest_mismatch")
    if (receipt.get("format_version") != 1 or receipt.get("kind") != "gnomonitoring-synthetic-source-receipt" or
            receipt.get("synthetic_only") is not True or receipt.get("source_frozen_before_dump") is not True or
            receipt.get("source_snapshot_equality_verified") is not False or receipt.get("source_postgres_image") != PG_IMAGE or
            not isinstance(receipt.get("source_postgres_version"), str) or not receipt["source_postgres_version"].startswith("16.")):
        raise SnapshotError("invalid_synthetic_receipt_metadata")
    if receipt.get("provenance") != source_provenance(receipt.get("fixture_names")):
        raise SnapshotError("synthetic_receipt_provenance_mismatch")
    if receipt.get("snapshot_sha256") != sha256(canonical_bytes(receipt.get("snapshot"))):
        raise SnapshotError("synthetic_snapshot_digest_mismatch")
    actual = {name: sha256((bundle / name).read_bytes()) for name in ("database.dump", "globals.sql", "toc.list", "manifest.json", "SHA256SUMS")}
    if receipt.get("bundle_files") != actual or receipt.get("archive_sha256") != actual["database.dump"]:
        raise SnapshotError("synthetic_receipt_archive_mismatch")
    receipt["receipt_sha256"] = digest
    return receipt
