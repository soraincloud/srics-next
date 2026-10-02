"""Check that downloadable source cannot include library data or linked secrets."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("source_bundle", Path(__file__).resolve().parents[1] / "prepare-open-source.py")
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)


class SourcePrivacyChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="srics-source-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        self.base = ["LICENSE", "NOTICE", "scripts/build.sh", "scripts/prepare-open-source.py", "go.mod", "web/package-lock.json"]
        for name in self.base:
            self.write(name, "synthetic project source\n")
        self.stage(*self.base)

    def write(self, name, text):
        p = self.root / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        return p

    def stage(self, *names):
        subprocess.run(["git", "add", "--", *names], cwd=self.root, check=True)

    def export(self):
        names = source.source_paths(self.root)
        manifest = {"project": "SRICS Next", "commit": "", "dirty": False, "files": source.fingerprint(self.root, names)}
        output = self.root / "export"
        output.mkdir()
        source.write_archive(self.root, output, names, manifest, "synthetic notices")
        unpacked = self.root / "unpacked"
        unpacked.mkdir()
        with tarfile.open(output / "source.tar.gz") as tar:
            for member in tar:
                self.assertTrue(member.isfile())
                self.assertNotIn("..", Path(member.name).parts)
                dest = unpacked / member.name
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_bytes(tar.extractfile(member).read())
                dest.chmod(member.mode)
        return unpacked / "srics-next"

    def test_untracked_private_files_and_build_output_are_excluded(self):
        self.write("data/private.json", "synthetic private data")
        self.write("config.json", "synthetic private configuration")
        self.write("docs/personal-note.md", "untracked personal note")
        self.write("internal/webui/dist/legal/source.tar.gz", "previous build")
        self.stage("internal/webui/dist/legal/source.tar.gz")
        self.assertEqual(source.source_paths(self.root), sorted(self.base))
        exported = self.export()
        self.assertFalse((exported / "data").exists())
        self.assertFalse((exported / "config.json").exists())
        self.assertFalse((exported / "docs/personal-note.md").exists())

    def test_sensitive_tracked_filenames_fail_closed(self):
        for name in ["docs/config.json", "docs/SRICS-recovery.json", "docs/backup.sricsbackup", "internal/identity.pem"]:
            with self.subTest(name=name):
                self.write(name, "synthetic only")
                self.stage(name)
                with self.assertRaisesRegex(ValueError, "Private file"):
                    source.source_paths(self.root)
                subprocess.run(["git", "rm", "-q", "-f", "--", name], cwd=self.root, check=True)

    def test_linked_files_cannot_enter_archive(self):
        secret = self.write("outside-secret", "synthetic only")
        link = self.root / "docs/linked.md"
        link.parent.mkdir()
        link.symlink_to(secret)
        self.stage("docs/linked.md")
        with self.assertRaisesRegex(ValueError, "symlinks"):
            source.source_paths(self.root)

    def test_export_rebuild_detects_edits_and_new_source_without_parent_git(self):
        exported = self.export()
        self.assertFalse(source.is_git_root(exported))
        names = source.source_paths(exported)
        self.assertFalse(source.git_identity(exported, names)["dirty"])
        (exported / "NOTICE").write_text("changed source notice")
        (exported / "scripts/extra.py").write_text("print('synthetic source')\n")
        names = source.source_paths(exported)
        self.assertIn("scripts/extra.py", names)
        self.assertTrue(source.git_identity(exported, names)["dirty"])
        private_link = exported / "docs"
        private_link.symlink_to(self.root)
        with self.assertRaisesRegex(ValueError, "symlinks"):
            source.source_paths(exported)

    def test_frontend_change_during_build_invalidates_original_snapshot(self):
        self.write("web/src/App.vue", "synthetic frontend source")
        self.stage("web/src/App.vue")
        names = source.source_paths(self.root)
        identity = source.git_identity(self.root, names)
        snapshot = {"files": source.fingerprint(self.root, names), **identity}
        self.write("web/src/App.vue", "changed after frontend compilation")
        with self.assertRaisesRegex(ValueError, "Source changed"):
            source.check_snapshot(snapshot, source.fingerprint(self.root, names), identity)


if __name__ == "__main__":
    unittest.main()
