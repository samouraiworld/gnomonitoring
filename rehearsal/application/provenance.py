"""Copy only verified Git blobs into the disposable backend build context."""

import hashlib
from pathlib import Path

REVISION = "6c670f835951ac6512cb95068a3d8a955677a319"


def freeze_backend(root, destination, command, refused):
    """Never pass the operator's working directory/configuration to docker build."""
    listing, _ = command(["git", "ls-tree", "-rz", "--full-tree", REVISION, "--", "backend"])
    destination.mkdir(mode=0o700)
    files = {}
    for entry in filter(None, listing.split("\0")):
        metadata, path = entry.split("\t", 1)
        mode, kind, digest = metadata.split()
        relative = Path(path).relative_to("backend")
        if kind != "blob" or mode not in {"100644", "100755"} or ".." in relative.parts:
            raise refused("unsupported_backend_source_entry")
        source = root / path
        if source.is_symlink() or not source.is_file():
            raise refused("backend_source_changed")
        data = source.read_bytes()
        actual = hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()
        if actual != digest:
            raise refused("backend_source_changed")
        target = destination / relative
        target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        target.write_bytes(data)
        target.chmod(0o700 if mode == "100755" else 0o600)
        files[str(relative)] = hashlib.sha256(data).hexdigest()
    if not files or "Dockerfile" not in files or "main.go" not in files:
        raise refused("backend_context_incomplete")
    combined = hashlib.sha256("".join(path + "\0" + files[path] + "\n" for path in sorted(files)).encode()).hexdigest()
    return {"revision": REVISION, "context_sha256": combined, "files": files}
