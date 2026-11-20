"""Give tracked files and their directories content-derived nanosecond mtimes."""
import hashlib
import os
from pathlib import Path
import subprocess

# 60 bits span 36 years before 2017, so Go never sees a recently modified input.
BASE_NS = 1483228800 * 1_000_000_000


def timestamp(data):
    return BASE_NS - (int.from_bytes(hashlib.sha256(data).digest()[:8], 'big') & ((1 << 60) - 1))


def stamp(root, paths):
    directories = {root}
    for name in paths:
        path = root / name
        data = os.readlink(path).encode() if path.is_symlink() else path.read_bytes()
        ns = timestamp(data)
        os.utime(path, ns=(ns, ns), follow_symlinks=False)
        if path.lstat().st_mtime_ns != ns:
            raise ValueError('filesystem lost timestamp precision: ' + str(path))
        directories.update(p for p in path.parents if p == root or root in p.parents)
    # Include untracked entries so added or renamed fixtures invalidate results.
    for path in sorted(directories, key=lambda p: len(p.parts), reverse=True):
        entries = []
        for child in sorted(path.iterdir()):
            stat = child.lstat()
            target = os.readlink(child) if child.is_symlink() else ''
            entries.append((child.name, stat.st_mode, stat.st_size, stat.st_mtime_ns, target))
        ns = timestamp(repr(entries).encode())
        os.utime(path, ns=(ns, ns))
        if path.stat().st_mtime_ns != ns:
            raise ValueError('filesystem lost timestamp precision: ' + str(path))


def tracked_worktree_files(root):
    raw = subprocess.check_output(['git', 'ls-files', '-t', '-z'], cwd=root).decode()
    paths = []
    for entry in raw.split('\0'):
        if not entry:
            continue
        status, _, name = entry.partition(' ')
        # Sparse checkouts mark excluded tracked files as skip-worktree ('S').
        if status != 'S':
            paths.append(name)
    return paths


def refresh_index(root):
    # Re-sync .git/index stat metadata with the stamped mtimes so downstream
    # `git diff --name-only` on blob:none clones neither hits the Git < 2.53
    # diffcore_skip_stat_unmatch segfault nor prefetches excluded blobs.
    res = subprocess.run(
        ['git', 'update-index', '-q', '--ignore-submodules', '--refresh'],
        cwd=root,
        check=False,
    )
    if res.returncode not in (0, 1):
        raise subprocess.CalledProcessError(res.returncode, res.args)


def normalize(root):
    stamp(root, tracked_worktree_files(root))
    refresh_index(root)


if __name__ == '__main__':
    normalize(Path.cwd())
