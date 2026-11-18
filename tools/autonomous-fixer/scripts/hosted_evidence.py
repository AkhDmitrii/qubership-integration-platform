import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import stat


PREPARATION = ("bootstrap.log", "bootstrap.log.exit", "dependencies.log", "stand.log", "disk.txt", "controller.log")
RESULT = ("report.json", "config.json", "provider-usage.json", "metrics.json")
STAGE = ("profile.json", "process.json", "response-raw.txt", "observed-agent.json")
PUBLIC_CONFIG = ("task_id", "task_kind", "workflow", "hosted")
PROFILE_FIELDS = ("Runtime", "Model", "Effort")
DIR_FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
FILE_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
MAX_FILE_BYTES = 16 * 1024 * 1024
MAX_ARCHIVE_BYTES = 96 * 1024 * 1024
MAX_ENTRIES = 2000
MAX_DEPTH = 12


@contextmanager
def directory(root, parts, create=False):
    fd = os.dup(root)
    try:
        for part in parts:
            if create:
                try:
                    os.mkdir(part, mode=0o700, dir_fd=fd)
                except FileExistsError:
                    pass
            child = os.open(part, DIR_FLAGS, dir_fd=fd)
            os.close(fd)
            fd = child
        yield fd
    finally:
        os.close(fd)


def private_name(name):
    name = name.lower()
    return (
        name.startswith(".")
        or name in {"home", "auth", "credentials", "secrets", "auth.json", "credentials.json"}
        or name.endswith(("-home", ".db", ".db-wal", ".db-shm", ".sqlite", ".sqlite3", ".pem", ".key"))
    )


class Evidence:
    def __init__(self, destination):
        self.destination = destination
        self.files = []
        self.omissions = {}
        self.bytes = 0
        self.entries = 0
        self.status = None

    def omit(self, path, reason):
        record = self.omissions.setdefault(reason, {"count": 0, "examples": []})
        record["count"] += 1
        if len(record["examples"]) < 5:
            record["examples"].append("/".join(path))

    def save(self, path, data):
        with directory(self.destination, path[:-1], create=True) as parent:
            fd = os.open(path[-1], os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
            with os.fdopen(fd, "wb") as output:
                output.write(data)

    def file(self, root, path):
        name = path[-1]
        limit = min(MAX_FILE_BYTES, MAX_ARCHIVE_BYTES - self.bytes)
        try:
            stated = os.stat(name, dir_fd=root, follow_symlinks=False)
            if not stat.S_ISREG(stated.st_mode) or stated.st_nlink != 1:
                self.omit(path, "non_regular_or_linked")
                return
            if stated.st_size > limit:
                self.omit(path, "size_limit")
                return
            fd = os.open(name, FILE_FLAGS, dir_fd=root)
            with os.fdopen(fd, "rb") as source:
                opened = os.fstat(source.fileno())
                if not stat.S_ISREG(opened.st_mode) or opened.st_nlink != 1 or not os.path.samestat(stated, opened):
                    self.omit(path, "changed_during_read")
                    return
                data = source.read(limit + 1)
        except FileNotFoundError:
            return
        except OSError:
            self.omit(path, "unreadable")
            return
        if len(data) > limit:
            self.omit(path, "size_limit")
            return
        if data.startswith((b"SQLite format 3\x00", b"\x37\x7f\x06\x82", b"\x37\x7f\x06\x83")):
            self.omit(path, "sqlite")
            return
        if path in (("result", "config.json"), ("result", "report.json")):
            try:
                parsed = json.loads(data)
                if not isinstance(parsed, dict):
                    raise ValueError("Expected an object")
            except (UnicodeDecodeError, ValueError, RecursionError):
                self.omit(path, "invalid_json")
                return
            if name == "config.json":
                projected = {key: parsed[key] for key in PUBLIC_CONFIG if isinstance(parsed.get(key), (str, bool))}
                for key in ("profile", "review_profile"):
                    profile = parsed.get(key)
                    if isinstance(profile, dict):
                        projected[key] = {field: profile[field] for field in PROFILE_FIELDS if isinstance(profile.get(field), str)}
                data = (json.dumps(projected, separators=(",", ":")) + "\n").encode()
            elif isinstance(parsed.get("status"), str):
                self.status = parsed["status"][:100]
        if len(data) > limit:
            self.omit(path, "size_limit")
            return
        self.save(path, data)
        self.bytes += len(data)
        self.files.append({"path": "/".join(path), "size": len(data), "sha256": hashlib.sha256(data).hexdigest()})

    @contextmanager
    def source_dir(self, root, path):
        try:
            fd = os.open(path[-1], DIR_FLAGS, dir_fd=root)
        except FileNotFoundError:
            yield None
            return
        except OSError:
            self.omit(path, "unsafe_directory")
            yield None
            return
        try:
            yield fd
        finally:
            os.close(fd)

    def walk(self, root, path, depth):
        if depth > MAX_DEPTH:
            self.omit(path, "depth_limit")
            return
        with os.scandir(root) as contents:
            for entry in contents:
                self.entries += 1
                if self.entries > MAX_ENTRIES:
                    self.omit(path, "entry_limit")
                    return
                child = (*path, entry.name)
                if private_name(entry.name):
                    self.omit(child, "private_path")
                elif entry.is_dir(follow_symlinks=False):
                    with self.source_dir(root, child) as nested:
                        if nested is not None:
                            self.walk(nested, child, depth + 1)
                else:
                    self.file(root, child)

    def collect(self, source):
        with self.source_dir(source, ("preparation",)) as prep:
            if prep is not None:
                for name in PREPARATION:
                    self.file(prep, ("preparation", name))
        with self.source_dir(source, ("result",)) as result:
            if result is None:
                return
            for name in RESULT:
                self.file(result, ("result", name))
            with os.scandir(result) as contents:
                for index, entry in enumerate(contents):
                    if index >= MAX_ENTRIES:
                        self.omit(("result",), "entry_limit")
                        break
                    if re.fullmatch(r"step-\d{4}-author", entry.name):
                        path = ("result", entry.name)
                        with self.source_dir(result, path) as step:
                            if step is not None:
                                for name in STAGE:
                                    self.file(step, (*path, name))
            with self.source_dir(result, ("result", "evidence")) as evidence:
                if evidence is not None:
                    self.walk(evidence, ("result", "evidence"), 1)


def stage(source, destination):
    if any(".." in path.parts or not path.is_absolute() or path == Path("/") for path in (source, destination)):
        raise ValueError("Supply absolute evidence paths without parent traversal")
    if destination.is_relative_to(source) or source.is_relative_to(destination):
        raise ValueError("The staged evidence directory must be separate from the pilot directory")
    root = os.open("/", DIR_FLAGS)
    try:
        with directory(root, destination.parent.parts[1:]) as parent:
            os.mkdir(destination.name, mode=0o700, dir_fd=parent)
            with directory(parent, (destination.name,)) as target:
                evidence = Evidence(target)
                try:
                    with directory(root, source.parts[1:]) as pilot:
                        evidence.collect(pilot)
                except FileNotFoundError:
                    evidence.omit((), "pilot_unavailable")
                evidence.save(("manifest.json",), (json.dumps({
                    "reported_status": evidence.status,
                    "files": sorted(evidence.files, key=lambda file: file["path"]),
                    "omissions": evidence.omissions,
                }, indent=2) + "\n").encode())
    finally:
        os.close(root)


def main():
    parser = argparse.ArgumentParser(description="Stage the files to retain from a hosted pilot")
    parser.add_argument("pilot", type=Path)
    parser.add_argument("evidence", type=Path)
    args = parser.parse_args()
    try:
        stage(args.pilot, args.evidence)
    except (OSError, ValueError) as error:
        parser.exit(1, f"{error}\n")


if __name__ == "__main__":
    main()
