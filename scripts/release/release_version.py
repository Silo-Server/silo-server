#!/usr/bin/env python3
"""Check a server release version and compute its Docker tags.

Release tags are vX.Y.Z, vX.Y.Z-beta.N or vX.Y.Z-rc.N. See docs/release-versioning.md.

    release_version.py check VERSION        key=value lines for $GITHUB_OUTPUT
    release_version.py docker-tags VERSION  one Docker tag per line

Both read the repository's v* tags with git.
"""

import argparse
import re
import subprocess
import sys

TAG = re.compile(r"v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(beta|rc)\.([1-9][0-9]*))?")
# Within one X.Y.Z, betas come first, then release candidates, then the release.
STAGES = ["beta", "rc", "stable"]
STABLE = STAGES.index("stable")


class ReleaseError(Exception):
    pass


def parse(name):
    """Return a sortable key (major, minor, patch, stage, number) for a release tag, else None.

    A stable release sorts after all of its betas and release candidates.
    """
    match = TAG.fullmatch(name)
    if not match:
        return None
    major, minor, patch, stage, number = match.groups()
    return (int(major), int(minor), int(patch), STAGES.index(stage or "stable"), int(number or 0))


def text(key):
    core = f"{key[0]}.{key[1]}.{key[2]}"
    return core if is_stable(key) else f"{core}-{STAGES[key[3]]}.{key[4]}"


def is_stable(key):
    return key[3] == STABLE


def release_keys(tags):
    return sorted(key for key in map(parse, tags) if key)


def check(tags, version):
    key = parse(version)
    if not key:
        raise ReleaseError(f"Version must look like 0.24.0, 1.0.0-beta.1 or 1.0.0-rc.1, got {version!r}.")
    existing = release_keys(tags)
    if key in existing:
        raise ReleaseError(f"v{text(key)} already exists.")
    if existing and key < existing[-1]:
        raise ReleaseError(f"v{text(key)} is older than the newest release, v{text(existing[-1])}.")
    # A beta's or release candidate's notes start at the previous release of any kind;
    # a stable release's notes cover everything since the previous stable one.
    older = [k for k in existing if k < key and (not is_stable(key) or is_stable(k))]
    return {
        "version": text(key),
        "tag": f"v{text(key)}",
        "prerelease": "false" if is_stable(key) else "true",
        "previous_tag": f"v{text(older[-1])}" if older else "",
    }


def docker_tags(tags, version):
    """Docker tags for a published release.

    latest moves to the newest release, and to a beta or release candidate only
    while no 1.x release exists yet (those of 1.0.0). X.Y and X move only to the
    newest release in their line, so a fix to an older line never takes them
    back. Every main build moves dev instead (see docker.yml).
    """
    key = parse(version)
    if not key:
        raise ReleaseError(f"Version must look like 0.24.0, 1.0.0-beta.1 or 1.0.0-rc.1, got {version!r}.")
    existing = release_keys([*tags, f"v{text(key)}"])
    out = [text(key)]
    if is_stable(key):
        same_minor = [k for k in existing if is_stable(k) and k[:2] == key[:2]]
        if key == same_minor[-1]:
            out.append(f"{key[0]}.{key[1]}")
        same_major = [k for k in existing if is_stable(k) and k[0] == key[0]]
        if key[0] >= 1 and key == same_major[-1]:
            out.append(f"{key[0]}")
    # Of the betas and release candidates, only 1.0.0's compete for latest,
    # and only until a 1.x release exists.
    v1_released = any(is_stable(k) and k[0] >= 1 for k in existing)
    candidates = [k for k in existing if is_stable(k) or (k[:3] == (1, 0, 0) and not v1_released)]
    if candidates and key == candidates[-1]:
        out.append("latest")
    return out


def git_tags():
    return subprocess.run(
        ["git", "tag", "--list", "v*"], check=True, capture_output=True, text=True
    ).stdout.split()


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=["check", "docker-tags"])
    parser.add_argument("version")
    args = parser.parse_args(argv)
    try:
        if args.command == "check":
            for name, value in check(git_tags(), args.version).items():
                print(f"{name}={value}")
        else:
            print("\n".join(docker_tags(git_tags(), args.version)))
    except ReleaseError as error:
        print(f"::error::{error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
