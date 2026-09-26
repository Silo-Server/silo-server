#!/usr/bin/env python3
"""Regression checks for the web consumer extractor."""
import json
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/apiv2-ledger/extract_consumers.py"


def main():
    with tempfile.TemporaryDirectory() as tmp:
        output = Path(tmp) / "calls.json"
        subprocess.run(
            [sys.executable, str(SCRIPT), str(ROOT / "web/src"), tmp, tmp, str(output)],
            check=True,
            cwd=ROOT,
        )
        calls = json.loads(output.read_text())
    votes = {
        (call["method"], call["path"])
        for call in calls
        if call["file"] == "src/api/v2/watchTogetherSuggestions.ts"
        and "suggestions/{x}/vote" in call["path"]
    }
    expected = {
        ("POST", "/api/v2/watch-together/rooms/{x}/suggestions/{x}/vote"),
        ("DELETE", "/api/v2/watch-together/rooms/{x}/suggestions/{x}/vote"),
    }
    if not expected <= votes:
        raise AssertionError(f"conditional operation keys missing: {expected - votes}")


if __name__ == "__main__":
    main()
