#!/usr/bin/env python3
"""Install verified build tools into .tools, without changing system settings."""
import hashlib, json, os, pathlib, re, shutil, subprocess, sys, tarfile, urllib.request, zipfile, xml.etree.ElementTree as ET

ROOT = pathlib.Path(__file__).resolve().parents[1]
TOOLS = ROOT / '.tools'
TOOLS.mkdir(exist_ok=True)

def fetch(url):
    with urllib.request.urlopen(urllib.request.Request(url, headers={'User-Agent': 'JunGo-build/1.0'}), timeout=60) as r:
        return r.read()

def download(url, name, digest=None, algorithm='sha256'):
    dest = TOOLS / name
    if not dest.exists():
        temp = dest.with_suffix(dest.suffix + '.part')
        print('Downloading', name, flush=True)
        with urllib.request.urlopen(url, timeout=90) as r, temp.open('wb') as f:
            shutil.copyfileobj(r, f)
        temp.replace(dest)
    if digest:
        h = hashlib.new(algorithm)
        with dest.open('rb') as f:
            for block in iter(lambda: f.read(1024 * 1024), b''):
                h.update(block)
        if h.hexdigest().lower() != digest.lower():
            dest.unlink()
            raise RuntimeError('Checksum mismatch for ' + name)
    return dest

def go():
    if (TOOLS / 'go/bin/go').exists(): return
    releases = json.loads(fetch('https://go.dev/dl/?mode=json'))
    release = next(r for r in releases if r['stable'])
    host = 'darwin' if sys.platform == 'darwin' else 'linux'
    arch = 'arm64' if os.uname().machine in ('arm64', 'aarch64') else 'amd64'
    item = next(f for f in release['files'] if f['os'] == host and f['arch'] == arch and f['kind'] == 'archive')
    archive = download('https://go.dev/dl/' + item['filename'], item['filename'], item['sha256'])
    with tarfile.open(archive) as f: f.extractall(TOOLS, filter='data')
    (TOOLS / 'go-version.json').write_text(json.dumps(item, indent=2))
    print('Go installed:', item['version'], flush=True)

def java():
    if (TOOLS / 'jdk').exists(): return
    host = 'mac' if sys.platform == 'darwin' else 'linux'
    arch = 'aarch64' if os.uname().machine in ('arm64', 'aarch64') else 'x64'
    url = f'https://api.adoptium.net/v3/assets/latest/21/hotspot?architecture={arch}&image_type=jdk&os={host}'
    try:
        item = json.loads(fetch(url))[0]['binary']['package']
    except Exception:
        # The OpenJDK archive is an independent official distribution fallback.
        page = fetch('https://jdk.java.net/archive/').decode()
        platform = 'macos' if host == 'mac' else 'linux'
        jarch = 'aarch64' if arch == 'aarch64' else 'x64'
        link = re.search(r'https://download.java.net/[^" ]*openjdk-21[^" ]*_' + platform + '-' + jarch + r'_bin.tar.gz', page).group(0)
        item = {'link': link, 'name': link.rsplit('/', 1)[1], 'checksum': fetch(link + '.sha256').decode().strip().split()[0]}
    archive = download(item['link'], item['name'], item['checksum'])
    target = TOOLS / 'jdk-extract'; target.mkdir(exist_ok=True)
    with tarfile.open(archive) as f: f.extractall(target, filter='data')
    entry = next(target.iterdir())
    home = entry / 'Contents/Home' if host == 'mac' else entry
    os.symlink(home.relative_to(TOOLS), TOOLS / 'jdk')
    print('JDK installed', flush=True)

def android():
    if (TOOLS / 'android-sdk/cmdline-tools/latest/bin/sdkmanager').exists(): return
    repository = ET.fromstring(fetch('https://dl.google.com/android/repository/repository2-1.xml'))
    packages = [p for p in repository.iter('remotePackage') if p.attrib['path'].startswith('cmdline-tools;') and 'latest' not in p.attrib['path']]
    package = max(packages, key=lambda p: tuple(int(p.findtext('revision/' + key) or '0') for key in ('major','minor','micro')))
    host = 'macosx' if sys.platform == 'darwin' else 'linux'
    archive = next(a for a in package.findall('archives/archive') if a.findtext('host-os') == host)
    name = archive.findtext('complete/url')
    checksum = archive.find('complete/checksum')
    path = download('https://dl.google.com/android/repository/' + name, name, checksum.text, checksum.attrib.get('type', 'sha1'))
    sdk = TOOLS / 'android-sdk'; sdk.mkdir(exist_ok=True)
    temp = TOOLS / 'android-extract'; temp.mkdir(exist_ok=True)
    with zipfile.ZipFile(path) as f: f.extractall(temp)
    dest = sdk / 'cmdline-tools/latest'; dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.move(str(temp / 'cmdline-tools'), dest)
    for file in (dest / 'bin').iterdir(): file.chmod(0o755)
    print('Android command line tools installed', flush=True)

if __name__ == '__main__':
    for name in sys.argv[1:] or ['go', 'java']:
        {'go': go, 'java': java, 'android': android}[name]()
