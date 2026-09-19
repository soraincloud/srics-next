"""Copy Homebrew executables, their non-system dylibs and license notices."""
import json
import pathlib
import shutil
import subprocess
import sys

resources = pathlib.Path(sys.argv[1])
target = resources / 'bin' / 'tools'
seen = {}

def output(*args):
    return subprocess.check_output(args, text=True)

def copy_notice(source, destination):
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.exists():
        destination.chmod(0o644)
    shutil.copyfile(source, destination)
    destination.chmod(0o644)

def bundle(source):
    source = pathlib.Path(source).resolve()
    dest = target / source.name
    if dest.name in seen:
        if seen[dest.name] != source:
            raise RuntimeError(f'Duplicate library name: {dest.name}')
        return dest
    seen[dest.name] = source
    shutil.copy2(source, dest)
    dest.chmod(0o755)
    if '.dylib' in dest.name:
        subprocess.run(['install_name_tool', '-id', '@loader_path/' + dest.name, str(dest)], check=True)
    rpaths = []
    lines = output('otool', '-l', str(source)).splitlines()
    for i, line in enumerate(lines):
        if 'cmd LC_RPATH' in line:
            rpaths.append(lines[i+2].strip().split(' (offset')[0].removeprefix('path '))
    for line in output('otool', '-L', str(source)).splitlines()[1:]:
        dep = line.strip().split(' (compatibility')[0]
        if dep.startswith(('/usr/lib/', '/System/Library/')):
            continue
        candidates = [pathlib.Path(dep)]
        if dep.startswith('@rpath/'):
            candidates = [pathlib.Path(r.replace('@loader_path', str(source.parent)).replace('@executable_path', str(source.parent))) / dep.removeprefix('@rpath/') for r in rpaths]
        elif dep.startswith('@loader_path/'):
            candidates = [source.parent / dep.removeprefix('@loader_path/')]
        resolved = next((p.resolve() for p in candidates if p.exists()), None)
        if resolved is None:
            raise RuntimeError(f'Cannot resolve {dep} from {source}')
        if resolved == source:
            continue
        copied = bundle(resolved)
        subprocess.run(['install_name_tool', '-change', dep, '@loader_path/' + copied.name, str(dest)], check=True)
    # Copy package-local legal notices; code and resources stay in the bundle.
    if 'Cellar' in source.parts:
        index = source.parts.index('Cellar')
        package = pathlib.Path(*source.parts[:index+3])
        notices = resources / 'licenses' / (source.parts[index+1] + '-' + source.parts[index+2])
        for file in package.iterdir():
            if file.is_file() and file.name.upper().startswith(('COPYING', 'LICENSE', 'NOTICE', 'AUTHORS', 'PATENTS')):
                notices.mkdir(parents=True, exist_ok=True)
                copy_notice(file, notices / file.name)
    subprocess.run(['codesign', '--force', '--sign', '-', str(dest)], check=True)
    return dest

for name in ['cwebp', 'restic']:
    source = shutil.which(name)
    if not source:
        raise RuntimeError(f'Missing build tool: {name}')
    bundle(source)
for binary in target.iterdir():
    if binary.is_file():
        linked = output('otool', '-L', str(binary))
        if '/opt/homebrew/' in linked or '/usr/local/' in linked or '@rpath/' in linked:
            raise RuntimeError(f'Unbundled dependency: {binary}')

# Include license notices for the Go executable and its statically linked modules.
modules = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], text=True)
decoder = json.JSONDecoder()
while modules.strip():
    module, end = decoder.raw_decode(modules.lstrip())
    modules = modules.lstrip()[end:]
    directory = module.get('Dir')
    if not directory or module.get('Main'):
        continue
    destination = resources / 'licenses' / module['Path'].replace('/', '_')
    for file in pathlib.Path(directory).iterdir():
        if file.is_file() and file.name.upper().startswith(('LICENSE', 'COPYING', 'NOTICE', 'PATENTS')):
            destination.mkdir(parents=True, exist_ok=True)
            copy_notice(file, destination / file.name)
goroot = pathlib.Path(output('go', 'env', 'GOROOT').strip())
(resources / 'licenses' / 'go').mkdir(exist_ok=True)
go_license = next(p for p in [goroot / 'LICENSE', goroot.parent / 'LICENSE'] if p.is_file())
copy_notice(go_license, resources / 'licenses' / 'go' / 'LICENSE')
