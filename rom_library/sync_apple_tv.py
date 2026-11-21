# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Sync curated ROMs, or back up and restore a complete Apple TV RetroArch setup."""
import argparse
import fcntl
import json
import hashlib
from pathlib import Path
import sys
from urllib.parse import urlencode
from urllib.request import Request, urlopen
from uuid import uuid4

from roms import BASE, digest

REMOTE_ROOT = ('downloads', 'ROMS')


class RetroArchServer:
    def __init__(self, url):
        self.url = url.rstrip('/')

    def list(self, folder):
        query = urlencode({'path': folder})
        with urlopen(f'{self.url}/list?{query}', timeout=30) as response:
            entries = json.load(response)
        result = {}
        for entry in entries:
            name = entry['name']
            expected = folder + name
            if (not safe_name(name) or name in result
                    or entry['path'] not in (expected, expected + '/')):
                raise ValueError(f'Invalid remote listing entry: {entry!r}')
            result[name] = entry
        return result

    def download(self, remote, destination):
        """Stream a file to disk; return its byte count and SHA-256."""
        query = urlencode({'path': remote})
        sha = hashlib.sha256()
        size = 0
        with urlopen(f'{self.url}/download?{query}', timeout=120) as response:
            while chunk := response.read(1024 * 1024):
                destination.write(chunk)
                sha.update(chunk)
                size += len(chunk)
        return size, sha.hexdigest()

    def move(self, old, new):
        data = urlencode({'oldPath': old, 'newPath': new}).encode()
        with urlopen(f'{self.url}/move', data=data, timeout=30):
            pass

    def ensure_folder(self, parts, dry_run):
        folder = '/'
        for name in parts:
            child = folder + name + '/'
            entry = self.list(folder).get(name)
            if entry is None:
                if dry_run:
                    return None
                data = urlencode({'path': child}).encode()
                with urlopen(f'{self.url}/create', data=data, timeout=30):
                    pass
                entry = self.list(folder).get(name)
            if entry is None or entry['path'] != child:
                raise ValueError(f'Remote path is not a directory: {child}')
            folder = child
        return folder

    def upload(self, folder, path, size):
        boundary = uuid4().hex
        prefix = (
            f'--{boundary}\r\nContent-Disposition: form-data; name="path"\r\n\r\n'
            f'{folder}\r\n--{boundary}\r\n'
            f'Content-Disposition: form-data; name="files[]"; filename="{path.name}"\r\n'
            'Content-Type: application/octet-stream\r\n\r\n'
        ).encode('utf-8')
        suffix = f'\r\n--{boundary}--\r\n'.encode()
        with path.open('rb') as rom:
            def body():
                yield prefix
                yield from iter(lambda: rom.read(1024 * 1024), b'')
                yield suffix

            request = Request(f'{self.url}/upload', data=body(), headers={
                'Content-Type': f'multipart/form-data; boundary={boundary}',
                'Content-Length': str(len(prefix) + size + len(suffix)),
            })
            with urlopen(request, timeout=120):
                pass


def safe_name(name):
    return isinstance(name, str) and name not in ('', '.', '..') and not any(
        char in '/\\"' or ord(char) < 32 for char in name
    )


def load_export(export):
    summary = json.loads((export / 'summary.json').read_text())
    groups = {}
    for report in summary['consoles']:
        console = report['console']
        if not safe_name(console) or console in groups:
            raise ValueError(f'Invalid or duplicate console: {console!r}')
        folder = export / console
        entries = json.loads((folder / 'selection.json').read_text())
        if len(entries) != report['selected']:
            raise ValueError(f'Manifest count disagrees with summary: {console}')
        files = {}
        for entry in entries:
            name = entry['file']
            if not safe_name(name) or name in files:
                raise ValueError(f'Invalid or duplicate ROM name: {name!r}')
            path = folder / name
            if path.is_symlink() or not path.is_file() or path.stat().st_size != entry['bytes']:
                raise ValueError(f'Local ROM does not match manifest: {path}')
            files[name] = (path, entry)
        groups[console] = files
    return groups


def sync(server, groups, dry_run):
    skipped = 0
    pending = []
    for console, files in groups.items():
        if not files:
            continue
        parts = (*REMOTE_ROOT, console)
        folder = server.ensure_folder(parts, dry_run)
        remote = server.list(folder) if folder else {}
        for name, (path, entry) in files.items():
            existing = remote.get(name)
            if existing is None:
                pending.append((folder or '/' + '/'.join(parts) + '/', path, entry))
            elif existing.get('size') == entry['bytes']:
                skipped += 1
            else:
                raise ValueError(f'Remote name exists with a different size or type: {folder}{name}')

    for folder, path, entry in pending:
        if digest(path) != entry['export_sha256']:
            raise ValueError(f'Local ROM hash does not match manifest: {path}')

    for folder, path, entry in pending:
        if dry_run:
            print(f'Would upload {folder}{path.name}')
            continue
        server.upload(folder, path, entry['bytes'])
        remote = server.list(folder).get(path.name, {})
        if remote.get('size') != entry['bytes']:
            raise ValueError(f'Upload verification failed: {folder}{path.name}')
        print(f'Uploaded {folder}{path.name}', flush=True)
    action = 'Would upload' if dry_run else 'Uploaded'
    print(f'{action} {len(pending)} ROMs. Skipped {skipped} already present.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--server', default='http://192.168.0.249', help='RetroArch web file server URL')
    parser.add_argument('--export', type=Path, default=BASE.parent / 'ROMs' / 'AppleTV-Top40')
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--backup', action='store_true', help='Capture the current Apple TV setup into a local profile')
    mode.add_argument('--restore', action='store_true', help='Restore config, ROMs, playlists and art from the local profile')
    parser.add_argument('--profile', type=Path, default=BASE / 'retroarch-profile', help='Reusable local setup directory')
    parser.add_argument('--source', type=Path, help='With --backup, read a local RetroArch data folder instead of Apple TV')
    parser.add_argument('--include-saves', action='store_true', help='With --backup, also capture saves and save states')
    parser.add_argument('--dry-run', action='store_true', help='Preview without changing local files or the server')
    args = parser.parse_args()
    if (args.source or args.include_saves) and not args.backup:
        parser.error('--source and --include-saves require --backup')
    try:
        (BASE / 'state').mkdir(parents=True, exist_ok=True)
        with (BASE / 'state' / 'run.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            server = RetroArchServer(args.server)
            if args.backup or args.restore:
                from retroarch_profile import backup, restore
                if args.backup:
                    backup(server, args.profile, args.dry_run, args.source, args.include_saves)
                else:
                    restore(server, args.profile, args.dry_run)
            else:
                sync(server, load_export(args.export), args.dry_run)
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f'Sync stopped: {error}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
