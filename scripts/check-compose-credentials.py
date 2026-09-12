#!/usr/bin/env python3
"""Check Compose credential interpolation without starting any containers."""

import json
import os
from pathlib import Path
import subprocess
import unittest
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parent.parent
COMPOSE_FILES = ("docker-compose.yml", "docker-compose.dev.yml")


def read_compose(filename):
    return (ROOT / filename).read_text()


def render_compose(filename, password=None):
    # Ignore the caller's deployment settings and .env, including env_file
    # entries on services. These checks only need the Compose CLI.
    env = {
        "PATH": os.environ.get("PATH", os.defpath),
        "HOME": "/nonexistent",
        "DOCKER_CONFIG": "/nonexistent",
        "MEDIA_ROOT": "/tmp/silo-compose-test/media",
        "SECRET_KEY": "compose-test-only-secret-key-32-characters",
    }
    if password is not None:
        env["POSTGRES_PASSWORD"] = password
    return subprocess.run(
        [
            "docker", "compose",
            "--project-name", "silo-credential-test",
            "--project-directory", "/",
            "--env-file", "/dev/null",
            "-f", "-",
            "config", "--no-env-resolution", "--format", "json",
        ],
        input=read_compose(filename),
        capture_output=True,
        text=True,
        env=env,
        timeout=30,
    )


class ComposeCredentialsTest(unittest.TestCase):
    def test_missing_password_is_rejected(self):
        for filename in COMPOSE_FILES:
            with self.subTest(compose=filename):
                result = render_compose(filename)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("POSTGRES_PASSWORD", result.stderr)

    def test_empty_password_is_rejected(self):
        for filename in COMPOSE_FILES:
            with self.subTest(compose=filename):
                result = render_compose(filename, "")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("POSTGRES_PASSWORD", result.stderr)

    def test_explicit_password_reaches_both_services_unchanged(self):
        passwords = (
            ("hex", "0123456789abcdef" * 3),
            ("literal", "compose-test$literal"),
            ("existing_default", "silo"),
        )
        for filename in COMPOSE_FILES:
            for password_kind, password in passwords:
                with self.subTest(compose=filename, password_kind=password_kind):
                    result = render_compose(filename, password)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    services = json.loads(result.stdout)["services"]
                    # Compose escapes dollars when serialising its reusable
                    # config output, including JSON.
                    rendered_password = password.replace("$", "$$")
                    self.assertEqual(
                        services["postgres"]["environment"]["POSTGRES_PASSWORD"],
                        rendered_password,
                    )
                    database_url = services["silo"]["environment"]["DATABASE_URL"]
                    self.assertEqual(urlsplit(database_url).password, rendered_password)


if __name__ == "__main__":
    unittest.main()
