"""Build the pinned restic release with audited dependency updates, without
changing system Homebrew installations or the repository storage format.
"""
import argparse
import json
import os
import pathlib
import shutil
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser()
parser.add_argument("--audit", action="store_true", help="check reachable vulnerabilities before building")
args = parser.parse_args()
env = dict(os.environ, GOTOOLCHAIN="go1.27.1", CGO_ENABLED="0")
source = json.loads(subprocess.check_output(
    ["go", "mod", "download", "-json", "github.com/restic/restic@v0.19.1"], cwd=root, env=env))
expected = "h1:LQKkar5/gYxay1OCokAWPBNiLr1lTpt4fhdM4GWqSlY="
if source.get("Sum") != expected:
    raise RuntimeError("Unexpected restic source checksum")

tools = root / "bin/tools"
tools.mkdir(parents=True, exist_ok=True)
licenses = tools / "restic-licenses"
licenses.mkdir(exist_ok=True)
with tempfile.TemporaryDirectory(prefix="srics-restic-build-") as temp:
    work = pathlib.Path(temp) / "source"
    shutil.copytree(source["Dir"], work)
    for name in ("go.mod", "go.sum"):
        dest = work / name
        dest.chmod(0o644)
        shutil.copyfile(root / "scripts/restic" / name, dest)
    subprocess.run(["go", "mod", "download"], cwd=work, env=env, check=True)
    subprocess.run(["go", "mod", "verify"], cwd=work, env=env, check=True)
    if args.audit:
        reports = root / "reports"
        reports.mkdir(exist_ok=True)
        with (reports / "security-restic.txt").open("w") as report:
            subprocess.run(["go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.8.0",
                            "-tags", "disable_grpc_modules", "-show", "verbose", "./cmd/restic"],
                           cwd=work, env=env, stdout=report, stderr=subprocess.STDOUT, check=True)
    binary = pathlib.Path(temp) / "restic"
    subprocess.run(["go", "build", "-mod=readonly", "-trimpath", "-tags", "disable_grpc_modules",
                    "-o", str(binary), "./cmd/restic"], cwd=work, env=env, check=True)
    # Include notices for the upstream tool and its complete pinned module graph.
    modules = subprocess.check_output(["go", "list", "-m", "-json", "all"], cwd=work, env=env, text=True)
    decoder = json.JSONDecoder()
    while modules.strip():
        module, end = decoder.raw_decode(modules.lstrip())
        modules = modules.lstrip()[end:]
        directory = module.get("Dir")
        if not directory:
            continue
        folder = licenses / module["Path"].replace("/", "_")
        folder.mkdir(exist_ok=True)
        for file in pathlib.Path(directory).iterdir():
            if file.is_file() and file.name.upper().startswith(("LICENSE", "COPYING", "NOTICE", "PATENTS")):
                dest = folder / file.name
                if dest.exists():
                    dest.chmod(0o644)
                shutil.copyfile(file, dest)
                dest.chmod(0o644)
    for name in ("go.mod", "go.sum"):
        shutil.copyfile(work / name, licenses / name)
    (licenses / "SOURCE.json").write_text(json.dumps({
        "module": source["Path"], "version": source["Version"], "sum": expected,
        "toolchain": "go1.27.1", "build_tags": ["disable_grpc_modules"],
        "changes": "Dependency updates only; pinned go.mod/go.sum included. No upstream source changes."
    }, indent=2)+"\n")
    shutil.copyfile(binary, tools / "restic.new")
    (tools / "restic.new").chmod(0o755)
    (tools / "restic.new").replace(tools / "restic")
print("已构建随包 restic 0.19.1（固定安全依赖）。")
