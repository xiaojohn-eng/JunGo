#!/usr/bin/env python3
"""Package a clean Git HEAD and matching, locally built release artifacts."""

import argparse
import copy
import hashlib
import io
import os
import pathlib
import plistlib
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
DIST = ROOT / 'dist'
VERSION = '0.2.3-preview'
RELEASE = f'jungo-v{VERSION}'
SOURCE_NAME = f'jungo-source-v{VERSION}.tar.gz'
MAC_DMG = DIST / f'军哥互联-{VERSION}-arm64.dmg'
MAC_ZIP = DIST / '军哥互联.app.zip'
ANDROID_APK = ROOT / 'android/app/build/outputs/apk/release/app-release.apk'
LINUX_FILES = ('README.md', 'LICENSE', 'SECURITY.md', 'THIRD_PARTY_NOTICES.md',
               'docs/download-install.md', 'docs/deployment.md', 'docs/validation.md',
               'docs/chat.md', 'docs/publication-security.md', 'deploy/jungo-control.service',
               'deploy/jungo-device.service')
PRIVATE_NAMES = {'admin.token', 'identity.key', 'identity.crt', 'registry.json', 'local-api.json', 'local.properties'}
PRIVATE_PARTS = {'.git', '.tools', '.state', '.gradle', '.build', '__pycache__', 'dist', 'build', 'backups', 'private', 'qa-probe'}
PRIVATE_SUFFIXES = {'.key', '.pem', '.p12', '.pfx', '.jks', '.keystore', '.mobileprovision', '.token', '.db', '.sqlite', '.sqlite3', '.dump', '.backup', '.bak', '.apk', '.aar', '.log'}


def private_path(name):
    path = pathlib.PurePosixPath(name)
    return (path.is_absolute() or '..' in path.parts or bool(set(path.parts) & PRIVATE_PARTS)
            or path.name in PRIVATE_NAMES or path.suffix.lower() in PRIVATE_SUFFIXES
            or path.name == '.env' or (path.name.startswith('.env.') and path.name != '.env.example')
            or (path.name.startswith('qa-') and path.name.endswith('.json'))
            or name.startswith('docs/reports/'))


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT)


def clean_head():
    if git('status', '--porcelain=v1', '--untracked-files=all'):
        raise RuntimeError('Commit all source changes before packaging; ignored build outputs are allowed.')
    return git('rev-parse', 'HEAD').decode().strip(), int(git('show', '-s', '--format=%ct', 'HEAD'))


def committed_source():
    # Git supplies every source byte; local state and ignored files cannot enter.
    source = tarfile.open(fileobj=io.BytesIO(git('archive', '--format=tar', 'HEAD')), mode='r:')
    rejected = [m.name for m in source.getmembers() if private_path(m.name) or m.issym() or m.islnk()]
    if rejected:
        source.close()
        raise RuntimeError('Committed source contains forbidden paths or links: ' + ', '.join(rejected))
    return source


def add_member(output, source, member, name):
    entry = copy.copy(member)
    entry.name = name
    entry.uid = entry.gid = 0
    entry.uname = entry.gname = ''
    entry.pax_headers = {}
    output.addfile(entry, source.extractfile(member) if member.isfile() else None)


def package_source(source, stage):
    target = stage / SOURCE_NAME
    with tarfile.open(target, 'w:gz') as output:
        for member in source.getmembers():
            add_member(output, source, member, 'jungo-source/' + member.name)
    return target


def build_info(data, label):
    with tempfile.NamedTemporaryFile(prefix='jungo-release-check-', dir='/private/tmp') as temporary:
        temporary.write(data)
        temporary.flush()
        go = ROOT / '.tools/go/bin/go'
        command = str(go) if go.is_file() else 'go'
        result = subprocess.run([command, 'version', '-m', temporary.name], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f'{label}: cannot read Go build information.')
    settings = dict(re.findall(r'^\s*build\t([^=\s]+)=([^\s]+)', result.stdout, re.MULTILINE))
    dependencies = set(re.findall(r'^\s*dep\t([^\s]+)', result.stdout, re.MULTILINE))
    return settings, dependencies


def check_private_bytes(data, label):
    if any(value.encode() in data for value in (str(ROOT), str(pathlib.Path.home()))):
        raise RuntimeError(f'{label}: contains the builder home or checkout path.')


def validate_go(data, label, commit, goos, goarch, cli=True, require_revision=True):
    settings, dependencies = build_info(data, label)
    if 'github.com/rasky/go-lzo' in dependencies:
        raise RuntimeError(f'{label}: includes the unresolved go-lzo redistribution dependency.')
    if require_revision and (settings.get('vcs.revision') != commit or settings.get('vcs.modified') != 'false'):
        raise RuntimeError(f'{label}: rebuild from clean Git HEAD after committing.')
    if settings.get('GOOS') != goos or settings.get('GOARCH') != goarch:
        raise RuntimeError(f'{label}: wrong target architecture.')
    if cli and f'JunGo v{VERSION}'.encode() not in data:
        raise RuntimeError(f'{label}: wrong application version.')
    check_private_bytes(data, label)


def require_artifact(path, label, commit_time):
    if not path.is_file() or not path.stat().st_size:
        raise RuntimeError(f'{label}: missing or empty artifact.')
    if path.stat().st_mtime < commit_time:
        raise RuntimeError(f'{label}: predates Git HEAD; rebuild it.')


def validate_mac(commit, commit_time):
    require_artifact(MAC_DMG, 'macOS DMG', commit_time)
    require_artifact(MAC_ZIP, 'macOS ZIP', commit_time)
    validate_go((DIST / 'jungo').read_bytes(), 'macOS backend', commit, 'darwin', 'arm64')
    with zipfile.ZipFile(MAC_ZIP) as app_zip:
        if app_zip.testzip() is not None:
            raise RuntimeError('macOS ZIP has a corrupt member.')
        contents = {}
        for member in app_zip.infolist():
            if member.is_dir():
                continue
            data = app_zip.read(member)
            check_private_bytes(data, 'macOS ZIP')
            for filename in ('Contents/MacOS/jungo', 'Contents/MacOS/JunGoMenu',
                             'Contents/Info.plist', 'Contents/_CodeSignature/CodeResources',
                             'Contents/Resources/AppIcon.icns',
                             'Contents/Resources/THIRD_PARTY_NOTICES.md',
                             'Contents/Resources/licenses/go-modules/index.json'):
                if member.filename.endswith('/' + filename):
                    contents[filename] = data
        required = {'Contents/MacOS/jungo', 'Contents/MacOS/JunGoMenu',
                    'Contents/Info.plist', 'Contents/_CodeSignature/CodeResources',
                    'Contents/Resources/AppIcon.icns',
                    'Contents/Resources/THIRD_PARTY_NOTICES.md',
                    'Contents/Resources/licenses/go-modules/index.json'}
        if set(contents) != required:
            raise RuntimeError('macOS ZIP lacks application executables or resources.')
        validate_go(contents['Contents/MacOS/jungo'], 'macOS ZIP backend', commit, 'darwin', 'arm64')

    subprocess.run(['hdiutil', 'verify', str(MAC_DMG)], check=True, capture_output=True)
    with tempfile.TemporaryDirectory(prefix='jungo-release-dmg-', dir='/private/tmp') as temp:
        mount = pathlib.Path(temp) / 'mount'
        mount.mkdir()
        subprocess.run(['hdiutil', 'attach', '-readonly', '-nobrowse', '-mountpoint', str(mount), str(MAC_DMG)],
                       check=True, capture_output=True)
        try:
            apps = list(mount.glob('*.app'))
            if len(apps) != 1:
                raise RuntimeError('macOS DMG must contain exactly one app.')
            app = apps[0]
            subprocess.run(['codesign', '--verify', '--deep', '--strict', str(app)], check=True, capture_output=True)
            info = plistlib.loads((app / 'Contents/Info.plist').read_bytes())
            if info.get('CFBundleShortVersionString') != VERSION.split('-')[0]:
                raise RuntimeError('macOS app bundle version does not match the release.')
            for filename, data in contents.items():
                if (app / filename).read_bytes() != data:
                    raise RuntimeError('macOS DMG and ZIP contain different app content.')
            for item in app.rglob('*'):
                if item.is_file() and not item.is_symlink():
                    check_private_bytes(item.read_bytes(), 'macOS DMG')
        finally:
            subprocess.run(['hdiutil', 'detach', str(mount)], check=True, capture_output=True)


def validate_android(commit, commit_time):
    require_artifact(ANDROID_APK, 'Android APK', commit_time)
    if ANDROID_APK.stat().st_mtime <= commit_time:
        raise RuntimeError('Android APK must be built after the release commit.')
    with zipfile.ZipFile(ANDROID_APK) as apk:
        if apk.testzip() is not None:
            raise RuntimeError('Android APK has a corrupt member.')
        names = apk.namelist()
        if 'AndroidManifest.xml' not in names or 'classes.dex' not in names:
            raise RuntimeError('Android APK lacks application files.')
        for resource in ('assets/THIRD_PARTY_NOTICES.md', 'assets/licenses/go-modules/index.json'):
            if resource not in names:
                raise RuntimeError(f'Android APK lacks {resource}.')
        if 'assets/jungo-release-commit.txt' not in names or apk.read('assets/jungo-release-commit.txt').strip() != commit.encode():
            raise RuntimeError('Android APK does not record this release commit.')
        go_library = 'lib/arm64-v8a/libgojni.so'
        if go_library not in names:
            raise RuntimeError('Android APK lacks its arm64 Go library.')
        for name in names:
            if not name.endswith('/'):
                check_private_bytes(apk.read(name), 'Android APK')
        validate_go(apk.read(go_library), 'Android native library', commit, 'android', 'arm64',
                    cli=False, require_revision=False)
    tools = ROOT / '.tools/android-sdk/build-tools/36.0.0'
    aapt, apksigner = tools / 'aapt', tools / 'apksigner'
    if not aapt.is_file() or not apksigner.is_file():
        raise RuntimeError('Android SDK build tools 36.0.0 are needed to verify the APK.')
    badging = subprocess.run([str(aapt), 'dump', 'badging', str(ANDROID_APK)],
                            check=True, capture_output=True, text=True).stdout
    if "name='com.junge.connect'" not in badging or f"versionName='{VERSION}'" not in badging:
        raise RuntimeError('Android package identity or version does not match the release.')
    env = os.environ.copy()
    env['JAVA_HOME'] = str(ROOT / '.tools/jdk')
    subprocess.run([str(apksigner), 'verify', '--verbose', str(ANDROID_APK)],
                   check=True, capture_output=True, env=env)


def package_linux(source, stage, arch):
    binary = DIST / f'jungo-linux-{arch}'
    prefix = f'{RELEASE}-linux-{arch}'
    target = stage / f'{prefix}.tar.gz'
    committed = {member.name: member for member in source.getmembers()}
    selected = set(LINUX_FILES) | {name for name in committed if name == 'licenses' or name.startswith('licenses/')}
    for name in selected:
        if name not in committed or not (committed[name].isfile() or committed[name].isdir()):
            raise RuntimeError(f'Release resource is missing from Git HEAD: {name}')
    with tarfile.open(target, 'w:gz') as output:
        entry = tarfile.TarInfo(prefix + '/jungo')
        entry.size = binary.stat().st_size
        entry.mode = 0o755
        with binary.open('rb') as payload:
            output.addfile(entry, payload)
        for name in sorted(selected):
            add_member(output, source, committed[name], prefix + '/' + name)
    return target


def checksum(path):
    digest = hashlib.sha256()
    with path.open('rb') as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--source-only', action='store_true', help='Package committed public source only')
    mode.add_argument('--all', action='store_true', help='Package source and verified installable artifacts')
    args = parser.parse_args()
    commit, commit_time = clean_head()
    if args.all:
        for arch in ('amd64', 'arm64'):
            binary = DIST / f'jungo-linux-{arch}'
            require_artifact(binary, f'Linux {arch}', commit_time)
            validate_go(binary.read_bytes(), f'Linux {arch}', commit, 'linux', arch)
        validate_mac(commit, commit_time)
        validate_android(commit, commit_time)

    DIST.mkdir(exist_ok=True)
    with committed_source() as source, tempfile.TemporaryDirectory(prefix='.jungo-pack-', dir=DIST) as temp:
        stage = pathlib.Path(temp)
        outputs = [package_source(source, stage)]
        if args.all:
            outputs.extend(package_linux(source, stage, arch) for arch in ('amd64', 'arm64'))
            for original, filename in ((MAC_DMG, f'{RELEASE}-macos-arm64.dmg'),
                                       (MAC_ZIP, f'{RELEASE}-macos-arm64.app.zip'),
                                       (ANDROID_APK, f'{RELEASE}-android-arm64.apk')):
                destination = stage / filename
                shutil.copyfile(original, destination)
                outputs.append(destination)
        checksum_name = 'SHA256SUMS.txt' if args.all else 'SOURCE-SHA256SUMS.txt'
        (stage / checksum_name).write_text(''.join(
            f'{checksum(path)}  {path.name}\n' for path in sorted(outputs, key=lambda path: path.name)))
        for path in outputs:
            path.replace(DIST / path.name)
        (stage / checksum_name).replace(DIST / checksum_name)
    print(f'Packaged {len(outputs)} asset(s) from clean Git HEAD {commit[:12]}; wrote {checksum_name}.')


if __name__ == '__main__':
    try:
        main()
    except (OSError, RuntimeError, subprocess.CalledProcessError, tarfile.TarError, zipfile.BadZipFile) as exc:
        raise SystemExit(f'Release packaging failed: {exc}') from None
