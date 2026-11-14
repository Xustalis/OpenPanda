# SPDX-License-Identifier: AGPL-3.0-or-later
"""Filesystem contracts for local installs and release version/asset selection."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[1]


class LocalInstallTests(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory(prefix="panda install ")
        self.addCleanup(self.work.cleanup)
        self.root = Path(self.work.name) / "source tree"
        self.home = Path(self.work.name) / "new home"
        for name, text in {
            "bin/panda": "new binary",
            "adapters/codex.py": "adapter",
            "extensions/voice/stt.py": "voice",
            "drivers/panda-notify": "driver",
        }.items():
            p = self.root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(text)
        self.path_bin = self.home / ".local/bin/panda"
        self.prefix = self.home / ".local/share/openpanda"

    def install(self):
        return subprocess.run(
            ["sh", str(REPO / "scripts/install-local.sh"), str(self.root), str(self.home)],
            capture_output=True, text=True,
        )

    def test_fresh_home_and_spaces_ship_all_helpers(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.path_bin.read_text(), "new binary")
        self.assertTrue(os.access(self.path_bin, os.X_OK))
        for prefix in [self.home / ".local", self.prefix]:
            self.assertEqual((prefix / "adapters/codex.py").read_text(), "adapter")
            self.assertEqual((prefix / "extensions/voice/stt.py").read_text(), "voice")
            self.assertTrue(os.access(prefix / "drivers/panda-notify", os.X_OK))

    def test_release_layout_keeps_link_and_atomic_binary_swap(self):
        real = self.prefix / "bin/panda"
        real.parent.mkdir(parents=True)
        real.write_text("old binary")
        self.path_bin.parent.mkdir(parents=True)
        self.path_bin.symlink_to(real)
        with real.open() as old:
            result = self.install()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(old.read(), "old binary")
        self.assertTrue(self.path_bin.is_symlink())
        self.assertEqual(self.path_bin.resolve(), real.resolve())
        self.assertEqual(real.read_text(), "new binary")

    def test_asset_failure_is_not_success_and_keeps_binary(self):
        self.path_bin.parent.mkdir(parents=True)
        self.path_bin.write_text("old binary")
        self.prefix.mkdir(parents=True)
        (self.prefix / "adapters").write_text("blocks directory creation")
        result = self.install()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.path_bin.read_text(), "old binary")
        self.assertNotIn("installed ", result.stdout)
        self.assertFalse(list(self.path_bin.parent.glob(".panda-install.*")))

    def test_missing_source_keeps_install_untouched(self):
        (self.root / "extensions/voice/stt.py").unlink()
        self.assertNotEqual(self.install().returncode, 0)
        self.assertFalse(self.home.exists())

    def test_custom_assets_and_config_survive_without_bytecode(self):
        self.prefix.mkdir(parents=True)
        config = self.prefix / "config.yaml"
        config.write_text("user config")
        custom = self.prefix / "adapters/custom.py"
        custom.parent.mkdir()
        custom.write_text("custom")
        cache = self.root / "adapters/__pycache__"
        cache.mkdir()
        (cache / "stale.pyc").write_text("cache")
        self.assertEqual(self.install().returncode, 0)
        self.assertEqual(config.read_text(), "user config")
        self.assertEqual(custom.read_text(), "custom")
        self.assertFalse((self.prefix / "adapters/__pycache__").exists())


class ReleaseToolingTests(unittest.TestCase):
    def test_source_version_override_lite_assets_and_checksum_refusal(self):
        # Fake only the compiler: run the real packaging/archive/checksum logic
        # against a deliberately different source version, without cross builds.
        with tempfile.TemporaryDirectory(prefix="panda-package-") as work:
            root = Path(work)
            for directory in ["scripts", "internal/version", "tools", "adapters", "drivers", "extensions/voice", "config"]:
                (root / directory).mkdir(parents=True, exist_ok=True)
            for name in ["package.sh", "verify-release.sh"]:
                shutil.copy2(REPO / "scripts" / name, root / "scripts" / name)
            (root / "internal/version/version.go").write_text('var Version = "9.8.7"\n')
            for name in ["adapters/codex.py", "extensions/voice/stt.py", "drivers/panda-notify", "config.example.yaml", "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "COMMERCIAL.md", "LICENSING.md"]:
                (root / name).write_text("fixture")
            compiler = root / "tools/go"
            compiler.write_text('#!/bin/sh\nwhile [ "$#" -gt 0 ]; do\n if [ "$1" = -o ]; then shift; printf fixture > "$1"; exit 0; fi\n shift\ndone\nexit 1\n')
            compiler.chmod(0o755)
            env = {k: v for k, v in os.environ.items() if not k.startswith("OPENPANDA_") and k != "VERSION"}
            env["PATH"] = str(root / "tools") + os.pathsep + env["PATH"]

            def run(script, *args):
                return subprocess.run(["sh", str(root / "scripts" / script), *args], env=env, text=True, capture_output=True)

            result = run("package.sh")
            self.assertEqual(result.returncode, 0, result.stderr)
            result = run("verify-release.sh")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("v9.8.7", result.stdout)
            lite = root / "dist/panda-9.8.7-lite-linux-armv7.tar.gz"
            saved = lite.read_bytes()
            lite.unlink()
            result = run("verify-release.sh")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(lite.name, result.stderr)
            lite.write_bytes(saved + b"tampered")
            self.assertNotEqual(run("verify-release.sh").returncode, 0)
            env["VERSION"] = "9.8.6"
            result = run("package.sh", "v9.8.5")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(run("verify-release.sh", "v9.8.5").returncode, 0)
            self.assertTrue((root / "dist/panda-9.8.5-darwin-arm64.tar.gz").exists())


if __name__ == "__main__":
    unittest.main()
