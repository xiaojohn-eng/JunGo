#!/usr/bin/env python3
"""Package committed public source; never walk the developer's filesystem."""
import argparse
import hashlib
import io
import pathlib
import subprocess
import tarfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
DIST = ROOT / 'dist'
VERSION = '0.2.2'
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


def package_source():
    # HEAD is the only source of archive bytes; ignored and untracked files cannot enter.
    data = subprocess.check_output(['git', 'archive', '--format=tar', 'HEAD'], cwd=ROOT)
    with tarfile.open(fileobj=io.BytesIO(data), mode='r:') as source:
        members = source.getmembers()
        rejected = [m.name for m in members if private_path(m.name) or m.issym() or m.islnk()]
        if rejected:
            raise RuntimeError('Refusing private paths or symlinks in committed source: ' + ', '.join(rejected))
        target = DIST / f'jungo-source-{VERSION}.tar.gz'
        temporary = target.with_suffix(target.suffix + '.part')
        try:
            with tarfile.open(temporary, 'w:gz') as output:
                for member in members:
                    payload = source.extractfile(member) if member.isfile() else None
                    member.name = 'jungo-source/' + member.name
                    member.uid = member.gid = 0
                    member.uname = member.gname = ''
                    member.pax_headers = {}
                    output.addfile(member, payload)
            temporary.replace(target)
        finally:
            temporary.unlink(missing_ok=True)
    return target


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-only', action='store_true', help='Do not package locally built binaries')
    args = parser.parse_args()
    DIST.mkdir(exist_ok=True)
    outputs = [package_source()]
    if not args.source_only:
        raise SystemExit('Source archive created. Binary redistribution is disabled pending the license review described in THIRD_PARTY_NOTICES.md. Use --source-only for this source release.')
    checksums = []
    for path in outputs:
        digest = hashlib.sha256()
        with path.open('rb') as handle:
            for block in iter(lambda: handle.read(1024 * 1024), b''):
                digest.update(block)
        checksums.append(f'{digest.hexdigest()}  {path.name}')
    (DIST / 'SOURCE-SHA256SUMS.txt').write_text('\n'.join(checksums) + '\n')
    print('Packaged committed public source; wrote SOURCE-SHA256SUMS.txt.')


if __name__ == '__main__':
    main()
