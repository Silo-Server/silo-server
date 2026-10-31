#!/usr/bin/env python3

import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    "release_version", Path(__file__).resolve().parent / "release_version.py"
)
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class CheckTest(unittest.TestCase):
    def test_first_release(self):
        self.assertEqual(
            {"version": "0.24.0", "tag": "v0.24.0", "prerelease": "false", "previous_tag": ""},
            release.check(["v-not-a-release"], "0.24.0"),
        )

    def test_leading_v_is_optional(self):
        self.assertEqual("v0.24.0", release.check([], "v0.24.0")["tag"])

    def test_release_candidate_notes_start_at_the_previous_release(self):
        result = release.check(["v0.24.0"], "1.0.0-rc.1")
        self.assertEqual(("true", "v0.24.0"), (result["prerelease"], result["previous_tag"]))
        self.assertEqual("v1.0.0-rc.1", release.check(["v0.24.0", "v1.0.0-rc.1"], "1.0.0-rc.2")["previous_tag"])

    def test_stable_notes_start_at_the_previous_stable_release(self):
        tags = ["v0.24.0", "v1.0.0-rc.1", "v1.0.0-rc.2"]
        self.assertEqual("v0.24.0", release.check(tags, "1.0.0")["previous_tag"])

    def test_release_candidates_sort_numerically(self):
        tags = [f"v1.0.0-rc.{n}" for n in range(1, 10)]
        self.assertEqual("v1.0.0-rc.9", release.check(tags, "1.0.0-rc.10")["previous_tag"])

    def test_refuses_bad_versions(self):
        for version in ["1.0", "1.0.0-RC1", "1.0.0-beta.1", "1.0.0-rc.0", "01.0.0"]:
            with self.subTest(version), self.assertRaisesRegex(release.ReleaseError, "look like"):
                release.check([], version)

    def test_refuses_existing_and_older_versions(self):
        with self.assertRaisesRegex(release.ReleaseError, "already exists"):
            release.check(["v0.24.0"], "0.24.0")
        with self.assertRaisesRegex(release.ReleaseError, "older than"):
            release.check(["v1.0.0-rc.1"], "0.25.0")
        with self.assertRaisesRegex(release.ReleaseError, "older than"):
            release.check(["v1.0.0"], "1.0.0-rc.3")


class DockerTagsTest(unittest.TestCase):
    def test_zero_releases_move_latest(self):
        self.assertEqual(["0.24.0", "0.24", "latest"], release.docker_tags([], "0.24.0"))
        self.assertEqual(["0.25.0", "0.25", "latest"], release.docker_tags(["v0.24.0"], "0.25.0"))

    def test_one_zero_release_candidates_move_latest(self):
        self.assertEqual(["1.0.0-rc.1", "latest"], release.docker_tags(["v0.24.0"], "1.0.0-rc.1"))
        self.assertEqual(["1.0.0-rc.2", "latest"], release.docker_tags(["v0.24.0", "v1.0.0-rc.1"], "1.0.0-rc.2"))

    def test_stable_release(self):
        tags = ["v0.24.0", "v1.0.0-rc.1"]
        self.assertEqual(["1.0.0", "1.0", "1", "latest"], release.docker_tags(tags, "1.0.0"))
        self.assertEqual(["1.0.1", "1.0", "1", "latest"], release.docker_tags([*tags, "v1.0.0"], "1.0.1"))

    def test_later_release_candidates_get_only_their_own_tag(self):
        self.assertEqual(["1.1.0-rc.1"], release.docker_tags(["v1.0.0"], "1.1.0-rc.1"))

    def test_hotfix_to_an_older_line_keeps_only_its_minor_tag(self):
        self.assertEqual(["1.0.1", "1.0"], release.docker_tags(["v1.0.0", "v1.1.0"], "1.0.1"))
        self.assertEqual(["0.24.1", "0.24"], release.docker_tags(["v0.24.0", "v0.25.0"], "0.24.1"))

    def test_republishing_is_stable(self):
        tags = ["v0.24.0", "v1.0.0-rc.1", "v1.0.0"]
        self.assertEqual(["1.0.0", "1.0", "1", "latest"], release.docker_tags(tags, "v1.0.0"))


if __name__ == "__main__":
    unittest.main()
