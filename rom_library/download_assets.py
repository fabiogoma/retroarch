"""Download explicit Internet Archive files or the pinned console bezel sources.

No assets are bundled in Git. Downloads are streamed, checked, and installed
without overwriting existing files. Python standard library only.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import ssl
import sys
import tempfile
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request, urlopen

BASE = Path(__file__).resolve().parent
CONSOLES = set(json.loads((BASE / 'consoles.json').read_text()))
MAX_BYTES = 32 * 1024**3


def relative_name(value):
    if (not isinstance(value, str) or not value or '\\' in value
            or any(ord(c) < 32 for c in value)
            or any(p in ('', '.', '..') or ':' in p for p in value.split('/'))
            or PurePosixPath(value).is_absolute()):
        raise ValueError(f'Unsafe relative path: {value!r}')
    return PurePosixPath(value)


def destination(root, name):
    relative = relative_name(name)
    if not (relative.parts[0] in CONSOLES | {'artwork'}
            or (relative.parts[:2] == ('rom_library', 'bezels')
                and len(relative.parts) == 3 and relative.suffix == '.png')):
        raise ValueError('Destination must be inside a console folder, artwork/, '
                         'or rom_library/bezels/<name>.png')
    if root.is_symlink():
        raise ValueError(f'Symlink destination root: {root}')
    current = root
    for part in relative.parts:
        current /= part
        if current.is_symlink():
            raise ValueError(f'Symlink destination: {current}')
    return current


def request(url):
    context = ssl.create_default_context()
    # Some python.org macOS installs have no Python-specific CA bundle.
    # Use the installed system roots while preserving explicit CA overrides.
    if (sys.platform == 'darwin' and ssl.get_default_verify_paths().cafile is None
            and not any(os.environ.get(k) for k in ('SSL_CERT_FILE', 'SSL_CERT_DIR'))
            and Path('/etc/ssl/cert.pem').is_file()):
        context.load_verify_locations('/etc/ssl/cert.pem')
    return urlopen(Request(url, headers={'User-Agent': 'RetroArchAppleTV/1.0'}),
                   timeout=120, context=context)


def metadata(item):
    if not isinstance(item, str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*', item):
        raise ValueError(f'Invalid Internet Archive item identifier: {item!r}')
    with request('https://archive.org/metadata/' + item) as response:
        data = json.load(response)
    if not isinstance(data, dict) or data.get('error') or not isinstance(data.get('files'), list):
        raise ValueError(f'Internet Archive item unavailable: {item}')
    return data['files']


def checksum(value, algorithm):
    length = hashlib.new(algorithm).digest_size * 2
    if not isinstance(value, str) or not re.fullmatch(r'[0-9a-fA-F]{' + str(length) + '}', value):
        raise ValueError(f'Invalid {algorithm} checksum: {value!r}')
    return value.lower()


def archive_plan(manifest):
    if not isinstance(manifest, dict) or manifest.get('version') != 1:
        raise ValueError('Manifest must have version 1 and a files array')
    entries = manifest.get('files')
    if not isinstance(entries, list) or not entries:
        raise ValueError('Add explicit item/file/destination entries to the manifest files array')
    cache, plan = {}, []
    for entry in entries:
        item, name, target = entry['item'], entry['file'], entry['destination']
        relative_name(name)
        relative_name(target)
        if item not in cache:
            cache[item] = metadata(item)
        matches = [f for f in cache[item] if f.get('name') == name]
        if len(matches) != 1:
            raise ValueError(f'File missing or ambiguous in Archive metadata: {item}/{name}')
        info = matches[0]
        size = int(info['size'])
        if not 0 <= size <= MAX_BYTES:
            raise ValueError(f'File size outside 0..{MAX_BYTES}: {name}')
        hashes = {}
        for algorithm in ('sha1', 'md5'):
            if info.get(algorithm):
                hashes[algorithm] = checksum(info[algorithm], algorithm)
        if entry.get('sha256'):
            hashes['sha256'] = checksum(entry['sha256'], 'sha256')
        if not hashes:
            raise ValueError(f'No checksum available for {item}/{name}')
        url = f'https://archive.org/download/{item}/' + quote(name, safe='/')
        plan.append({'url': url, 'destination': target, 'size': size, 'hashes': hashes})
    return plan


def bezel_plan():
    plan = []
    for slug, entry in json.loads((BASE / 'bezels/sources.json').read_text()).items():
        url = entry['source']
        if not url.startswith('https://raw.githubusercontent.com/libretro/overlay-borders/'):
            raise ValueError(f'Unexpected bezel source: {url}')
        plan.append({'url': url, 'destination': f'rom_library/bezels/{slug}.png',
                     'size': None, 'hashes': {'sha256': checksum(entry['sha256'], 'sha256')}})
    return plan


def verify(path, entry):
    hashes = {algorithm: hashlib.new(algorithm) for algorithm in entry['hashes']}
    size = 0
    with path.open('rb') as stream:
        while chunk := stream.read(1024 * 1024):
            size += len(chunk)
            if size > (entry['size'] if entry['size'] is not None else MAX_BYTES):
                raise ValueError(f'Size mismatch: {path}')
            for digest in hashes.values():
                digest.update(chunk)
    if entry['size'] is not None and size != entry['size']:
        raise ValueError(f'Size mismatch: {path}')
    if any(digest.hexdigest() != entry['hashes'][algorithm] for algorithm, digest in hashes.items()):
        raise ValueError(f'Checksum mismatch: {path}')


def download(plan, root, dry_run=False):
    root = root.absolute()
    targets, pending = set(), []
    # Validate the complete plan and existing files before making any changes.
    for entry in plan:
        path = destination(root, entry['destination'])
        key = str(path).casefold()
        if key in targets:
            raise ValueError(f'Duplicate destination: {path}')
        targets.add(key)
        if path.exists():
            verify(path, entry)
            print('Already verified:', path)
        else:
            pending.append((path, entry))
    for path, entry in pending:
        if dry_run:
            print(f'Would download {entry["url"]} -> {path}')
            continue
        destination(root, entry['destination'])
        path.parent.mkdir(parents=True, exist_ok=True)
        # Random staging file prevents partial content from becoming a ROM.
        with tempfile.NamedTemporaryFile(prefix='.asset-', suffix='.download',
                                         dir=path.parent, delete=False) as output:
            temporary = Path(output.name)
            try:
                size = 0
                with request(entry['url']) as source:
                    while chunk := source.read(1024 * 1024):
                        size += len(chunk)
                        if size > (entry['size'] if entry['size'] is not None else MAX_BYTES):
                            raise ValueError(f'Download larger than expected: {path}')
                        output.write(chunk)
                output.flush()
                verify(temporary, entry)
                destination(root, entry['destination'])
                # Atomic, no-clobber activation; concurrent downloads cannot overwrite.
                os.link(temporary, path)
            finally:
                temporary.unlink(missing_ok=True)
        print('Downloaded and verified:', path)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--list', metavar='ITEM', help='List files and checksums in an Archive item')
    mode.add_argument('--manifest', type=Path, help='JSON file containing explicit Archive downloads')
    mode.add_argument('--bezels', action='store_true', help='Fetch six hash-pinned upstream console PNGs')
    parser.add_argument('--root', type=Path, default=BASE.parent)
    parser.add_argument('--dry-run', action='store_true', help='Read metadata and preview; write nothing')
    args = parser.parse_args(argv)
    try:
        if args.list:
            print(json.dumps(metadata(args.list), indent=2, ensure_ascii=False))
        else:
            plan = bezel_plan() if args.bezels else archive_plan(json.loads(args.manifest.read_text()))
            download(plan, args.root, args.dry_run)
    except HTTPError as error:
        message = ('Access denied; stopping. This source requires authorization.'
                   if error.code in (401, 403) else str(error))
        print(f'Download stopped: {message}', file=sys.stderr)
        return 1
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f'Download stopped: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
