"""Package the build's Git-tracked source and runtime notices, never library data."""
import argparse
import fnmatch
import gzip
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile

ROOT_FILES = {
    "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "SECURITY.md", "CONTRIBUTING.md",
    "README.md", "CHANGELOG.md", ".gitignore", "go.mod", "go.sum", "start.command",
}
SOURCE_DIRS = {"cmd", "internal", "desktop", "web", "scripts", "docs", "testdata", ".github", "LICENSES"}
PRIVATE_PATTERNS = [
    "config.json", "credentials.json", "*.db*", "*.sqlite*", "*.age", "*.pem",
    "*.p12", "*.pfx", "*.key", "*.sricsbackup", "*-recovery.json",
    "*recovery-key*.json", "SRICS*恢复*.json", ".env", ".env.*",
]
EXCLUDED_PREFIXES = ("internal/webui/dist/", "web/node_modules/", "desktop/tests/.build/")


def run(root, *args):
    return subprocess.check_output(args, cwd=root, text=True).strip()


def is_git_root(root):
    result = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=root, capture_output=True, text=True)
    return result.returncode == 0 and Path(result.stdout.strip()).resolve() == root.resolve()


def source_paths(root):
    if is_git_root(root):
        names = subprocess.check_output(["git", "ls-files", "-z"], cwd=root).decode().split("\0")
    else:
        exported = root / "SOURCE_BUILD.json"
        if not exported.is_file() or json.loads(exported.read_text()).get("project") != "SRICS Next":
            raise ValueError("Build from a Git checkout or an exported SRICS source archive")
        names = list(ROOT_FILES)
        for directory in SOURCE_DIRS:
            base = root / directory
            if base.is_symlink():
                raise ValueError("Source symlinks are not supported: " + directory)
            for parent, folders, files in os.walk(base, followlinks=False):
                for folder in list(folders):
                    child = Path(parent) / folder
                    name = child.relative_to(root).as_posix()
                    if folder == "__pycache__" or (name + "/").startswith(EXCLUDED_PREFIXES):
                        folders.remove(folder)
                    elif child.is_symlink():
                        raise ValueError("Source symlinks are not supported: " + name)
                names.extend((Path(parent) / name).relative_to(root).as_posix() for name in files)
        names = [name for name in names if (root / name).exists() or (root / name).is_symlink()]
    paths = []
    for name in sorted(set(filter(None, names))):
        p = PurePosixPath(name)
        if p.is_absolute() or ".." in p.parts:
            raise ValueError("Unsafe source path")
        if name.startswith(EXCLUDED_PREFIXES) or "__pycache__" in p.parts:
            continue
        if name not in ROOT_FILES and p.parts[0] not in SOURCE_DIRS:
            raise ValueError("Unapproved tracked path: " + name)
        if any(fnmatch.fnmatch(p.name, pattern) for pattern in PRIVATE_PATTERNS):
            raise ValueError("Private file must not be tracked: " + name)
        path = root / name
        if any((root / Path(*p.parts[:i])).is_symlink() for i in range(1, len(p.parts) + 1)):
            raise ValueError("Source symlinks are not supported: " + name)
        if not path.is_file():
            raise ValueError("Source file is missing: " + name)
        paths.append(name)
    for name in ["LICENSE", "NOTICE", "scripts/build.sh", "scripts/prepare-open-source.py", "go.mod", "web/package-lock.json"]:
        if name not in paths:
            raise ValueError("Stage source files before building; missing: " + name)
    return paths


def fingerprint(root, names):
    return {name: {"sha256": hashlib.sha256((root / name).read_bytes()).hexdigest(),
                   "executable": bool((root / name).stat().st_mode & 0o111)} for name in names}


def git_identity(root, names):
    if not is_git_root(root):
        original = json.loads((root / "SOURCE_BUILD.json").read_text())
        return {"commit": original["commit"], "dirty": original["dirty"] or fingerprint(root, names) != original["files"]}
    commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=root, capture_output=True, text=True)
    return {"commit": commit.stdout.strip() if commit.returncode == 0 else "",
            "dirty": bool(run(root, "git", "status", "--porcelain"))}


def check_snapshot(snapshot, files, identity):
    if snapshot["files"] != files or any(snapshot[k] != identity[k] for k in identity):
        raise ValueError("Source changed during build; rebuild before distribution.")


def notices_in(directory):
    return sorted(p for p in directory.iterdir() if p.is_file()
                  and p.name.upper().startswith(("LICENSE", "COPYING", "NOTICE", "PATENTS", "AUTHORS")))


def runtime_notices(root):
    sections = [("SRICS Next third-party components", (root / "THIRD_PARTY_NOTICES.md").read_text()),
                ("Feather Icons", (root / "LICENSES/Feather.txt").read_text())]
    raw = subprocess.check_output(["go", "list", "-deps", "-json", "./cmd/srics"], cwd=root, text=True)
    decoder = json.JSONDecoder()
    modules = {}
    while raw.strip():
        package, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        module = package.get("Module")
        if module and not module.get("Main"):
            modules[module["Path"]] = module
    for module in sorted(modules.values(), key=lambda m: m["Path"]):
        if module.get("Main"):
            continue
        directory = module.get("Dir")
        if not directory:
            raise ValueError("Module source not downloaded: " + module["Path"])
        notices = notices_in(Path(directory))
        if not notices:
            raise ValueError("Module has no license notice: " + module["Path"])
        for file in notices:
            sections.append((module["Path"] + " " + module["Version"] + " / " + file.name, file.read_text()))
    goroot = Path(run(root, "go", "env", "GOROOT"))
    license_path = next(p for p in [goroot / "LICENSE", goroot.parent / "LICENSE"] if p.is_file())
    sections.append((run(root, "go", "version"), license_path.read_text()))
    lock = json.loads((root / "web/package-lock.json").read_text())
    for name, package in sorted(lock["packages"].items()):
        if not name or package.get("dev") or package.get("optional"):
            continue
        directory = root / "web" / name
        notices = notices_in(directory)
        if not notices:
            raise ValueError("Frontend runtime has no license notice: " + name)
        for file in notices:
            sections.append((name.removeprefix("node_modules/") + " " + package["version"] + " / " + file.name, file.read_text()))
    restic_notices = root / "bin/tools/restic-licenses"
    if not (restic_notices / "SOURCE.json").is_file():
        raise ValueError("Build restic before generating its notices")
    for file in sorted(restic_notices.rglob("*")):
        if file.is_file() and file.name not in {"go.mod", "go.sum"}:
            sections.append(("restic / " + str(file.relative_to(restic_notices)), file.read_text()))
    # Native image-library notices are also copied individually by macOS packaging.
    image_tool = shutil.which("cwebp")
    pending = [Path(image_tool).resolve()] if image_tool else []
    seen, packages = set(), set()
    while pending:
        file = pending.pop()
        if file in seen:
            continue
        seen.add(file)
        if "Cellar" in file.parts:
            index = file.parts.index("Cellar")
            package = Path(*file.parts[:index + 3])
            if package not in packages:
                packages.add(package)
                for notice in notices_in(package):
                    sections.append((package.parent.name + " " + package.name + " / " + notice.name, notice.read_text()))
        if shutil.which("otool"):
            result = subprocess.run(["otool", "-L", str(file)], capture_output=True, text=True)
            for line in result.stdout.splitlines()[1:]:
                dep = line.strip().split(" (compatibility")[0]
                if dep.startswith(("/opt/homebrew/", "/usr/local/")) and Path(dep).is_file():
                    pending.append(Path(dep).resolve())
    return "\n\n".join("=" * 72 + "\n" + title + "\n" + "=" * 72 + "\n" + text for title, text in sections) + "\n"


def write_archive(root, output, names, manifest, third_party):
    temp = output / "source.tar.gz.new"
    with temp.open("wb") as raw, gzip.GzipFile(fileobj=raw, filename="", mode="wb", mtime=0) as zipped, tarfile.open(fileobj=zipped, mode="w|") as tar:
        def add(name, data, executable=False):
            info = tarfile.TarInfo("srics-next/" + name)
            info.size, info.mode, info.mtime = len(data), 0o755 if executable else 0o644, 0
            tar.addfile(info, io.BytesIO(data))
        for name in names:
            data = (root / name).read_bytes()
            if hashlib.sha256(data).hexdigest() != manifest["files"][name]["sha256"]:
                raise ValueError("Source changed while archiving: " + name)
            add(name, data, manifest["files"][name]["executable"])
        add("SOURCE_BUILD.json", json.dumps(manifest, ensure_ascii=False, indent=2).encode() + b"\n")
        add("THIRD_PARTY_NOTICES.txt", third_party.encode())
    os.replace(temp, output / "source.tar.gz")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--verify", action="store_true", help="fail if source changed during the build")
    mode.add_argument("--snapshot", action="store_true", help="record inputs before frontend and service compilation")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    output = root / "internal/webui/dist/legal"
    names = source_paths(root)
    files = fingerprint(root, names)
    identity = git_identity(root, names)
    snapshot_path = root / "bin/source-inputs.json"
    if args.snapshot:
        snapshot_path.parent.mkdir(exist_ok=True)
        snapshot_path.write_text(json.dumps({"files": files, **identity}, ensure_ascii=False, indent=2) + "\n")
        print("已记录构建前源码快照。")
        return
    if args.verify:
        manifest = json.loads((output / "source-info.json").read_text())
        check_snapshot(manifest, files, identity)
        if hashlib.sha256((output / "source.tar.gz").read_bytes()).hexdigest() != manifest["sourceSHA256"]:
            raise SystemExit("Source archive checksum mismatch.")
        print("源码与构建输入一致。")
        return
    check_snapshot(json.loads(snapshot_path.read_text()), files, identity)
    release = json.loads((root / "internal/buildinfo/release.json").read_text())
    manifest = {"project": "SRICS Next", "license": "AGPL-3.0-only", **release, **identity, "files": files}
    output.mkdir(parents=True, exist_ok=True)
    third_party = runtime_notices(root)
    write_archive(root, output, names, manifest, third_party)
    manifest["sourceSHA256"] = hashlib.sha256((output / "source.tar.gz").read_bytes()).hexdigest()
    (output / "source-info.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
    for name in ["LICENSE", "NOTICE"]:
        shutil.copyfile(root / name, output / (name + ".txt"))
    (output / "THIRD_PARTY_NOTICES.txt").write_text(third_party)
    print("已收录当前源码及第三方许可：", len(names), "个源文件。")


if __name__ == "__main__":
    main()
