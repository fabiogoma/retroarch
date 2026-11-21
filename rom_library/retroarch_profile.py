"""Portable, hash-verified RetroArch snapshots for sync_apple_tv.py (stdlib only)."""
from datetime import datetime, timezone
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import tempfile
from uuid import uuid4

from roms import digest
from sync_apple_tv import safe_name

ROOTS = ('config', 'playlists', 'downloads/ROMS', 'thumbnails', 'overlays',
         'shaders', 'autoconfig', 'assets', 'system')
TEXT_SUFFIXES = {'.cfg', '.opt', '.rmp', '.lpl', '.slangp', '.glslp', '.cgp'}
DATA_HOME = re.compile(r'(?:/private)?/var/mobile/Containers/Data/Application/[0-9A-Fa-f-]{36}(?=/)')
BUNDLE_HOME = re.compile(r'(?:/private)?/var/(?:containers|mobile/Containers)/Bundle/Application/[0-9A-Fa-f-]{36}/[^/"\n]+\.app(?=/)')


def relative_path(value):
    if not isinstance(value, str):
        raise ValueError('Profile path must be a string')
    if not value or any(not safe_name(part) for part in value.split('/')):
        raise ValueError(f'Unsafe profile path: {value!r}')
    return PurePosixPath(value)


def local_path(root, relative):
    """Refuse symlinks in every component, including the profile itself."""
    path = root
    # Ancestors may include macOS's standard /var -> /private/var alias.
    if root.is_symlink():
        raise ValueError(f'Symlinks are not supported: {root}')
    for part in relative_path(relative).parts:
        path /= part
        if path.is_symlink():
            raise ValueError(f'Symlinks are not supported: {path}')
    return path


def portable(path):
    """Replace Apple installation UUIDs with RetroArch's supported home aliases."""
    if path.suffix.lower() not in TEXT_SUFFIXES:
        return
    content = path.read_text(encoding='utf-8')
    updated = BUNDLE_HOME.sub(':', DATA_HOME.sub('~', content))
    if updated != content:
        path.write_text(updated, encoding='utf-8')


def remote_inventory(server, roots):
    files, directories = {}, set()

    def walk(folder):
        directories.add(folder.strip('/'))
        for name, entry in sorted(server.list(folder).items()):
            path = folder + name
            # Validate even server doubles; never trust paths supplied by a listing.
            relative_path(path.lstrip('/'))
            if entry['path'] == path + '/':
                walk(path + '/')
            elif entry['path'] == path and type(entry.get('size')) is int and entry['size'] >= 0:
                files[path.lstrip('/')] = entry['size']
            else:
                raise ValueError(f'Invalid remote file or directory: {path}')

    for root in roots:
        folder = server.ensure_folder(relative_path(root).parts, dry_run=True)
        if folder:
            walk(folder)
    return files, directories


def local_inventory(source, roots):
    files, directories = {}, set()

    def walk(path, relative):
        local_path(source, str(path.relative_to(source)))
        if path.is_dir():
            directories.add(relative)
            for child in sorted(path.iterdir()):
                walk(child, relative + '/' + child.name)
        elif path.is_file():
            files[relative] = path.stat().st_size
        else:
            raise ValueError(f'Unsupported local file: {path}')

    for root in roots:
        path = local_path(source, root)
        if path.exists():
            walk(path, root)
    # Desktop-style data folders can keep the main cfg directly in the root.
    if 'config/retroarch.cfg' not in files and local_path(source, 'retroarch.cfg').is_file():
        files['config/retroarch.cfg'] = (source / 'retroarch.cfg').stat().st_size
        directories.add('config')
    return files, directories


def backup(server, profile, dry_run=False, source=None, include_saves=False):
    profile = profile.absolute()
    local_path(profile.parent, profile.name)
    previous = profile.with_name(profile.name + '.previous')
    local_path(previous.parent, previous.name)
    roots = (*ROOTS, 'saves', 'states') if include_saves else ROOTS
    if source:
        source = source.absolute()
        if (source == profile or source in profile.parents
                or profile in source.parents):
            raise ValueError('The source and profile directories must not contain each other')
        files, directories = local_inventory(source, roots)
    else:
        files, directories = remote_inventory(server, roots)
    if 'config/retroarch.cfg' not in files:
        raise ValueError('Source has no config/retroarch.cfg; refusing an incomplete setup')
    print(f'{"Would back up" if dry_run else "Backing up"} {len(files)} files '
          f'({sum(files.values()):,} bytes) to {profile}', flush=True)
    if dry_run:
        for name in sorted(files):
            print('Would back up /' + name)
        return

    profile.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.retroarch-backup-', dir=profile.parent) as temp:
        stage = Path(temp) / 'profile'
        data = stage / 'files'
        data.mkdir(parents=True)
        for name in sorted(directories):
            (data / name).mkdir(parents=True, exist_ok=True)
        manifest = {'version': 1, 'created_utc': datetime.now(timezone.utc).isoformat(),
                    'source': str(source) if source else server.url,
                    'roots': list(roots), 'directories': sorted(directories), 'files': {}}
        def capture_file(item):
            name, size = item
            destination = data / name
            destination.parent.mkdir(parents=True, exist_ok=True)
            if source:
                original = source / name
                if name == 'config/retroarch.cfg' and not original.exists():
                    original = source / 'retroarch.cfg'
                shutil.copyfile(original, destination)
                actual_size = destination.stat().st_size
                transferred_hash = digest(destination)
            else:
                for attempt in range(2):
                    with destination.open('wb') as stream:
                        actual_size, transferred_hash = server.download('/' + name, stream)
                    if actual_size == size:
                        break
                    if attempt == 0:
                        # History playlists can change after inventory. Refresh their
                        # size and retry once, still rejecting incomplete transfers.
                        remote = PurePosixPath('/' + name)
                        latest = server.list(str(remote.parent) + '/').get(remote.name, {})
                        if latest.get('path') != str(remote) or type(latest.get('size')) is not int:
                            raise ValueError(f'Source disappeared or changed type: /{name}')
                        size = latest['size']
            if actual_size != size:
                raise ValueError(f'Source changed or download was truncated: /{name}')
            portable(destination)
            return name, {'bytes': destination.stat().st_size, 'sha256': digest(destination)}, (actual_size, transferred_hash)

        config_original = None
        with ThreadPoolExecutor(max_workers=4) as pool:
            try:
                for index, (name, entry, original_hash) in enumerate(pool.map(capture_file, sorted(files.items())), 1):
                    manifest['files'][name] = entry
                    if name == 'config/retroarch.cfg':
                        config_original = original_hash
                    if index % 100 == 0 or index == len(files):
                        print(f'Backed up {index}/{len(files)} files', flush=True)
            except BaseException:
                pool.shutdown(wait=True, cancel_futures=True)
                raise
        if source:
            config = source / 'config/retroarch.cfg'
            if not config.exists():
                config = source / 'retroarch.cfg'
            config_current = (config.stat().st_size, digest(config))
        else:
            config_current = remote_digest(server, '/config/retroarch.cfg')
        if config_current != config_original:
            raise ValueError('Main configuration changed during backup; leave RetroArch idle and retry')
        (stage / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
        # Validate the staged copy before replacing the last complete snapshot.
        load_profile(stage)
        if profile.exists():
            if previous.exists():
                shutil.rmtree(previous)
            profile.rename(previous)
        try:
            stage.rename(profile)
        except OSError:
            if previous.exists() and not profile.exists():
                previous.rename(profile)
            raise
    print(f'Saved reusable setup: {profile}', flush=True)


def load_profile(profile):
    manifest = json.loads(local_path(profile, 'manifest.json').read_text())
    if manifest.get('version') != 1:
        raise ValueError('Unsupported profile version')
    roots = manifest['roots']
    if (not isinstance(roots, list) or not roots
            or any(root not in (*ROOTS, 'saves', 'states') for root in roots)):
        raise ValueError('Invalid profile roots')

    def checked(name):
        path = relative_path(name)
        if not any(path == PurePosixPath(root) or PurePosixPath(root) in path.parents for root in roots):
            raise ValueError(f'File is outside profile roots: {name}')
        return local_path(profile / 'files', name)

    directories = manifest['directories']
    for name in directories:
        if not checked(name).is_dir():
            raise ValueError(f'Missing profile directory: {name}')
    files = manifest['files']
    if 'config/retroarch.cfg' not in files:
        raise ValueError('Profile has no main RetroArch configuration')
    for name, entry in files.items():
        path = checked(name)
        if (not path.is_file() or path.stat().st_size != entry['bytes']
                or digest(path) != entry['sha256']):
            raise ValueError(f'Profile hash or size mismatch: {name}')
    return manifest


def remote_digest(server, remote):
    class Sink:
        def write(self, chunk):
            pass
    return server.download(remote, Sink())


def restore(server, profile, dry_run=False):
    manifest = load_profile(profile)
    files = manifest['files']
    pending, originals = [], {}
    listings = {}
    # Plan every file before writing anything; same-size changes also need hashes.
    for name, entry in sorted(files.items()):
        path = PurePosixPath('/' + name)
        folder = str(path.parent) + '/'
        if folder not in listings:
            available = server.ensure_folder(path.parent.parts[1:], dry_run=True)
            listings[folder] = server.list(folder) if available else {}
        existing = listings[folder].get(path.name)
        if existing:
            if existing['path'] != str(path) or 'size' not in existing:
                raise ValueError(f'Remote directory conflicts with file: {path}')
            actual = remote_digest(server, str(path))
            if actual == (entry['bytes'], entry['sha256']):
                continue
            originals[name] = actual
        pending.append(name)
    # Check empty-directory conflicts during planning too.
    for name in manifest['directories']:
        server.ensure_folder(relative_path(name).parts, dry_run=True)
    print(f'{"Would restore" if dry_run else "Restoring"} {len(pending)} files; '
          f'{len(files) - len(pending)} already identical.', flush=True)
    if dry_run:
        for name in pending:
            print(f'Would {"replace (with backup)" if name in originals else "upload"} /{name}')
        return

    # ROMs and dependencies first, playlists next, and main settings last.
    def order(name):
        return (3 if name == 'config/retroarch.cfg' else
                2 if name.startswith('config/') else
                1 if name.startswith('playlists/') else 0, name)

    ready_folders = set()
    for name in sorted(manifest['directories'], key=lambda value: (value.count('/'), value)):
        ready_folders.add(server.ensure_folder(relative_path(name).parts, dry_run=False))
    run_id = uuid4().hex
    with tempfile.TemporaryDirectory(prefix='retroarch-restore-') as temp:
        for index, name in enumerate(sorted(pending, key=order), 1):
            remote = PurePosixPath('/' + name)
            folder = str(remote.parent) + '/'
            if folder not in ready_folders:
                folder = server.ensure_folder(remote.parent.parts[1:], dry_run=False)
                ready_folders.add(folder)
            entry = files[name]
            original = local_path(profile / 'files', name)
            # Always stage under a unique name: the Apple server numbers duplicate uploads.
            staged = Path(temp) / ('restore-' + uuid4().hex + original.suffix)
            shutil.copyfile(original, staged)
            if (staged.stat().st_size, digest(staged)) != (entry['bytes'], entry['sha256']):
                raise ValueError(f'Profile changed during restore: {name}')
            server.upload(folder, staged, entry['bytes'])
            uploaded = folder + staged.name
            if remote_digest(server, uploaded) != (entry['bytes'], entry['sha256']):
                raise ValueError(f'Staged upload verification failed: {uploaded}')
            # Recheck before activation so in-memory config saves are not silently lost.
            existing = server.list(folder).get(remote.name)
            if name in originals and (not existing or remote_digest(server, str(remote)) != originals[name]):
                raise ValueError(f'Remote file changed during restore: {remote}')
            if name not in originals and existing:
                raise ValueError(f'Remote file appeared during restore: {remote}')
            saved = str(remote) + '.restore-backup-' + run_id if name in originals else None
            try:
                if saved:
                    server.move(str(remote), saved)
                    if remote_digest(server, saved) != originals[name]:
                        raise ValueError(f'Remote backup verification failed: {saved}')
                    print(f'Preserved {saved}', flush=True)
                server.move(uploaded, str(remote))
                if remote_digest(server, str(remote)) != (entry['bytes'], entry['sha256']):
                    raise ValueError(f'Restore verification failed: {remote}')
            except (OSError, ValueError):
                if saved:
                    current = server.list(folder)
                    if remote.name not in current and PurePosixPath(saved).name in current:
                        server.move(saved, str(remote))
                raise
            staged.unlink()
            print(f'Restored {index}/{len(pending)}: {remote}', flush=True)
    print('Restore complete. Fully restart RetroArch to load the restored settings.', flush=True)
