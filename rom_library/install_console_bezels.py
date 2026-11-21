"""Install downloaded console bezels via RetroArch's Apple TV file server.

Default is a read-only preview. Pass --install to upload missing files.
Existing files that differ are never overwritten.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
from urllib.parse import urlencode
from urllib.request import urlopen

from sync_apple_tv import RetroArchServer

BASE = Path(__file__).resolve().parent
ART = BASE / 'bezels'
STATE = BASE / 'state' / 'bezels'
CONSOLES = {
    'nes': ('nes', 'Nestopia', 'Nintendo - Nintendo Entertainment System'),
    'snes': ('snes', 'Snes9x', 'Nintendo - Super Nintendo Entertainment System'),
    'genesis': ('genesis', 'Genesis Plus GX', 'Sega - Mega Drive - Genesis'),
    'master-system': ('master system', 'Genesis Plus GX', 'Sega - Master System - Mark III'),
    'pc-engine': ('PC Engine', 'Beetle PCE Fast', 'NEC - PC Engine - TurboGrafx 16'),
    'n64': ('n64', 'Mupen64Plus-Next', 'Nintendo - Nintendo 64'),
}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def prepare():
    sources = json.loads((ART / 'sources.json').read_text())
    files = {}
    for slug, (folder, core, _) in CONSOLES.items():
        image = ART / (slug + '.png')
        if not image.is_file():
            raise ValueError('Missing artwork; run download_assets.py --bezels first: ' + str(image))
        if sha(image.read_bytes()) != sources[slug]['sha256']:
            raise ValueError(f'Artwork hash mismatch: {image}')
        preset = ART / (slug + '.cfg')
        preset.write_text(
            'overlays = 1\n'
            f'overlay0_overlay = "{slug}.png"\n'
            'overlay0_full_screen = true\n'
            'overlay0_normalized = true\n'
            'overlay0_rect = "0.0,0.0,1.0,1.0"\n'
            'overlay0_descs = 0\n'
        )
        override = ART / 'overrides' / core / (folder + '.cfg')
        override.parent.mkdir(parents=True, exist_ok=True)
        override.write_text(
            '# Console bezel for the matching ROM folder.\n'
            f'input_overlay = "~/Library/Caches/RetroArch/overlays/console-bezels/{slug}.cfg"\n'
            'input_overlay_enable = "true"\n'
            'input_overlay_enable_autopreferred = "false"\n'
            'input_overlay_auto_scale = "false"\n'
            'input_overlay_auto_rotate = "false"\n'
            'input_overlay_opacity = "1.000000"\n'
            'input_overlay_hide_when_gamepad_connected = "false"\n'
            'input_overlay_hide_in_menu = "true"\n'
            'input_overlay_pointer_enable = "false"\n'
        )
        for local in (image, preset):
            files['/overlays/console-bezels/' + local.name] = local
        files[f'/config/{core}/{folder}.cfg'] = override
    for name in ('LICENSE', 'sources.json'):
        files['/overlays/console-bezels/' + name] = ART / name
    # Upload artwork and presets first, enabling overrides last.
    return dict(sorted(files.items(), key=lambda item: item[0].startswith('/config/')))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--server', default='http://192.168.0.249')
    parser.add_argument('--install', action='store_true')
    args = parser.parse_args()
    files = prepare()
    server = RetroArchServer(args.server)

    def download(remote):
        url = server.url + '/download?' + urlencode({'path': remote})
        with urlopen(url, timeout=30) as response:
            return response.read()

    config = download('/config/retroarch.cfg')
    if not re.search(rb'^auto_overrides_enable\s*=\s*"true"', config, re.M):
        raise ValueError('Automatic config overrides must be enabled first.')
    if not re.search(rb'^aspect_ratio_index\s*=\s*"0"', config, re.M):
        raise ValueError('These bezels require the existing 4:3 display setting.')

    counts = {}
    for slug, (folder, core, system) in CONSOLES.items():
        entries = server.list('/config/' + core + '/')
        unexpected = [name for name in entries if name.endswith('.cfg')
                      and '/config/' + core + '/' + name not in files]
        if unexpected:
            raise ValueError(f'Check existing overrides before installing: {core}: {unexpected}')
        playlist = json.loads(download('/playlists/' + system + '.lpl'))
        for item in playlist['items']:
            rom = PurePosixPath(item['path'].split('#', 1)[0])
            if rom.parent.name != folder:
                raise ValueError(f'Unexpected ROM folder: {item["path"]}')
        counts[folder] = len(playlist['items'])

    pending = []
    for remote, local in files.items():
        path = PurePosixPath(remote)
        parent = str(path.parent) + '/'
        parts = path.parent.parts[1:]
        available = server.ensure_folder(parts, dry_run=True)
        existing = server.list(parent).get(path.name) if available else None
        if existing is not None:
            if download(remote) != local.read_bytes():
                raise ValueError(f'Refusing to overwrite different remote file: {remote}')
        else:
            pending.append((remote, local))
    print('Playlist coverage:', json.dumps(counts), flush=True)
    print(f'{len(pending)} new files; {len(files) - len(pending)} already identical.', flush=True)
    if not args.install:
        for remote, _ in pending:
            print('Would upload', remote)
        return

    STATE.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    backup = STATE / ('retroarch-before-install-' + timestamp + '.cfg')
    backup.write_bytes(config)
    report = {
        'server': server.url, 'started_utc': timestamp, 'installed': False,
        'backup': str(backup), 'main_config_sha256': sha(config),
        'playlist_games': counts, 'files': {}, 'visual_playback_verified': False,
        'scope': 'Folder overrides for the six currently configured console cores.',
    }
    report_path = STATE / 'installation.json'
    report_path.write_text(json.dumps(report, indent=2) + '\n')
    for remote, local in pending:
        path = PurePosixPath(remote)
        folder = server.ensure_folder(path.parent.parts[1:], dry_run=False)
        server.upload(folder, local, local.stat().st_size)
        if download(remote) != local.read_bytes():
            raise ValueError(f'Upload verification failed: {remote}')
        print('Uploaded and verified', remote, flush=True)
        report['files'][remote] = sha(local.read_bytes())
        report_path.write_text(json.dumps(report, indent=2) + '\n')
    for remote, local in files.items():
        if download(remote) != local.read_bytes():
            raise ValueError(f'Final verification failed: {remote}')
        report['files'][remote] = sha(local.read_bytes())
    if download('/config/retroarch.cfg') != config:
        raise ValueError('Main config changed during installation; inspect before continuing.')
    report.update(installed=True, main_config_unchanged=True,
                  finished_utc=datetime.now(timezone.utc).isoformat())
    report_path.write_text(json.dumps(report, indent=2) + '\n')
    print('All six console bezels installed. Reload content to activate.', flush=True)


if __name__ == '__main__':
    main()
