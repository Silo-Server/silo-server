#!/usr/bin/env python3
"""Check Compose credential handling without starting any containers."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parent.parent
COMPOSE_FILES = ("docker-compose.yml", "docker-compose.dev.yml")
GUARD_MESSAGE = "POSTGRES_PASSWORD is required"

# An external PostgreSQL and Redis override that removes the bundled services.
EXTERNAL_OVERRIDE = """\
services:
  postgres: !reset null
  redis: !reset null
  silo:
    depends_on: !reset {}
    environment:
      DATABASE_URL: postgres://silo:external-secret@db.example.test:5432/silo?sslmode=require
      REDIS_URL: redis://cache.example.test:6379
"""

PASSWORDS = (
    ("hex", "0123456789abcdef" * 3),
    ("literal_dollar", "compose-test$literal"),
    ("existing_default", "silo"),
)


def base_env(password=None):
    # Ignore the caller's deployment settings. These checks only need the
    # Compose CLI.
    env = {
        "PATH": os.environ.get("PATH", os.defpath),
        "HOME": "/nonexistent",
        "DOCKER_CONFIG": "/nonexistent",
        "MEDIA_ROOT": "/tmp/silo-compose-test/media",
        "SECRET_KEY": "compose-test-only-secret-key-32-characters",
    }
    if password is not None:
        env["POSTGRES_PASSWORD"] = password
    return env


def render(files, password=None, env_file="/dev/null", environ=None, resolve_env=False):
    """Render Compose files from an empty project directory.

    The empty directory keeps service env_file entries from reading a real
    .env file.
    """
    with tempfile.TemporaryDirectory() as project_dir:
        args = [
            "docker", "compose",
            "--project-name", "silo-credential-test",
            "--project-directory", project_dir,
            "--env-file", str(env_file),
        ]
        for name, content in files:
            path = Path(project_dir, name)
            path.write_text(content)
            args += ["-f", str(path)]
        args += ["config", "--format", "json"]
        if not resolve_env:
            args.append("--no-env-resolution")
        return subprocess.run(
            args,
            capture_output=True,
            text=True,
            env=environ if environ is not None else base_env(password),
            timeout=30,
        )


def render_compose(filename, password=None, env_file="/dev/null", environ=None):
    return render(
        [(filename, (ROOT / filename).read_text())], password, env_file, environ
    )


def unescape(value):
    # Compose escapes dollars when serialising its reusable config output.
    return value.replace("$$", "$")


def run_postgres_guard(service, password):
    """Run the rendered postgres entrypoint against a stub image entrypoint."""
    entrypoint = [unescape(part) for part in service["entrypoint"]]
    command = [unescape(part) for part in service["command"]]
    with tempfile.TemporaryDirectory() as bin_dir:
        stub = Path(bin_dir, "docker-entrypoint.sh")
        stub.write_text('#!/bin/sh\nprintf "%s\\n" "$POSTGRES_PASSWORD" "$@"\n')
        stub.chmod(0o755)
        env = {"PATH": f"{bin_dir}:{os.defpath}"}
        if password is not None:
            env["POSTGRES_PASSWORD"] = password
        return subprocess.run(
            entrypoint + command, capture_output=True, text=True, env=env, timeout=30
        )


def write_bootstrap_env(path, password):
    environ = {"PATH": os.environ.get("PATH", os.defpath)}
    if password is not None:
        environ["POSTGRES_PASSWORD"] = password
    return subprocess.run(
        [str(ROOT / "scripts" / "init-dev-env.sh"), str(path)],
        capture_output=True,
        text=True,
        env=environ,
        timeout=30,
    )


def read_dotenv_with_compose(path):
    """Load a dotenv file through Compose's own env_file parser."""
    probe = (
        "services:\n"
        "  probe:\n"
        "    image: scratch\n"
        f"    env_file: [{json.dumps(str(path))}]\n"
    )
    result = render(
        [("probe.yml", probe)],
        environ={"PATH": os.environ.get("PATH", os.defpath), "HOME": "/nonexistent"},
        resolve_env=True,
    )
    if result.returncode != 0:
        raise AssertionError(result.stderr)
    environment = json.loads(result.stdout)["services"]["probe"]["environment"]
    return {key: unescape(value) for key, value in environment.items()}


class ComposeCredentialsTest(unittest.TestCase):
    def test_bundled_postgres_refuses_missing_or_empty_password(self):
        for filename in COMPOSE_FILES:
            for password in (None, ""):
                with self.subTest(compose=filename, password=password):
                    result = render_compose(filename, password)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    postgres = json.loads(result.stdout)["services"]["postgres"]
                    guard = run_postgres_guard(postgres, password)
                    self.assertEqual(guard.returncode, 1, guard.stdout)
                    self.assertIn(GUARD_MESSAGE, guard.stderr)

    def test_explicit_password_reaches_both_services_unchanged(self):
        for filename in COMPOSE_FILES:
            for password_kind, password in PASSWORDS:
                with self.subTest(compose=filename, password_kind=password_kind):
                    result = render_compose(filename, password)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    services = json.loads(result.stdout)["services"]
                    postgres = services["postgres"]
                    self.assertEqual(
                        unescape(postgres["environment"]["POSTGRES_PASSWORD"]), password
                    )
                    database_url = services["silo"]["environment"]["DATABASE_URL"]
                    self.assertEqual(unescape(urlsplit(database_url).password), password)

                    guard = run_postgres_guard(postgres, password)
                    self.assertEqual(guard.returncode, 0, guard.stderr)
                    self.assertEqual(
                        guard.stdout.splitlines(),
                        [password, *postgres["command"]],
                    )

    def test_external_database_override_renders_without_bundled_password(self):
        result = render(
            [
                ("docker-compose.yml", (ROOT / "docker-compose.yml").read_text()),
                ("external.yml", EXTERNAL_OVERRIDE),
            ]
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        services = json.loads(result.stdout)["services"]
        self.assertEqual(set(services), {"silo"})
        self.assertNotIn("depends_on", services["silo"])
        self.assertEqual(
            urlsplit(services["silo"]["environment"]["DATABASE_URL"]).password,
            "external-secret",
        )

    def test_bootstrap_env_file_preserves_passwords(self):
        passwords = (
            ("generated", None),
            *PASSWORDS,
            ("reserved_url_characters", 'a@b#c/d%e?f:g "h\\i ü'),
            ("inner_backslashes", "a\\b\\\\c"),
        )
        for password_kind, password in passwords:
            with self.subTest(password_kind=password_kind), tempfile.TemporaryDirectory() as tmp:
                env_file = Path(tmp, ".env")
                bootstrap = write_bootstrap_env(env_file, password)
                self.assertEqual(bootstrap.returncode, 0, bootstrap.stderr)
                self.assertEqual(env_file.stat().st_mode & 0o777, 0o600)

                values = read_dotenv_with_compose(env_file)
                stored = values["POSTGRES_PASSWORD"]
                if password is None:
                    self.assertRegex(stored, r"^[0-9a-f]{48}$")
                else:
                    self.assertEqual(stored, password)
                source_url = urlsplit(values["DATABASE_URL"])
                self.assertEqual(source_url.hostname, "localhost")
                self.assertEqual(unquote(source_url.password), stored)

                # Render the bundled stack from the real .env file, without
                # the password in the process environment.
                result = render_compose(
                    "docker-compose.yml", env_file=env_file, environ=base_env()
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                postgres = json.loads(result.stdout)["services"]["postgres"]
                self.assertEqual(
                    unescape(postgres["environment"]["POSTGRES_PASSWORD"]), stored
                )

    def test_bootstrap_rejects_unrepresentable_passwords(self):
        for password, message in (
            ("it's", "single quote"),
            ("line\nbreak", "single quote"),
            ("abc\\", "end with a backslash"),
            ("abc\\\\", "end with a backslash"),
        ):
            with self.subTest(password=password), tempfile.TemporaryDirectory() as tmp:
                env_file = Path(tmp, ".env")
                result = write_bootstrap_env(env_file, password)
                self.assertEqual(result.returncode, 1)
                self.assertIn(message, result.stderr)
                self.assertFalse(env_file.exists())

    def test_bootstrap_does_not_overwrite_existing_env(self):
        with tempfile.TemporaryDirectory() as tmp:
            env_file = Path(tmp, ".env")
            env_file.write_text("SECRET_KEY=keep\n")
            result = write_bootstrap_env(env_file, None)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(env_file.read_text(), "SECRET_KEY=keep\n")


if __name__ == "__main__":
    unittest.main()
