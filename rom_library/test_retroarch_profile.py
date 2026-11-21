"""Exercise complete snapshots, repeat restores and failures without an Apple TV."""
from contextlib import redirect_stdout
import hashlib
import io
import json
from pathlib import Path
import tempfile
from threading import Event
import unittest
from unittest.mock import patch

from retroarch_profile import backup, load_profile, restore


class MemoryServer:
    url = 'http://apple-tv.test'

    def __init__(self, files=None):
        self.files = dict(files or {})
        self.directories = {'/'}
        self.writes = []
        self.fail_download = None
        self.fail_move = None
        self.corrupt_upload = False
        for path in self.files:
            self.ensure_folder(Path(path).parent.parts[1:], False)
        self.writes.clear()

    def list(self, folder):
        result = {}
        for path in sorted(self.directories):
            if path != '/' and str(Path(path.rstrip('/')).parent) + '/' == folder:
                name = Path(path.rstrip('/')).name
                result[name] = {'name': name, 'path': path}
        for path, data in self.files.items():
            parent = str(Path(path).parent).rstrip('/') + '/'
            if parent == folder:
                name = Path(path).name
                result[name] = {'name': name, 'path': path, 'size': len(data)}
        return result

    def ensure_folder(self, parts, dry_run):
        folder = '/'
        for part in parts:
            if folder + part in self.files:
                raise ValueError('Remote path is not a directory')
            folder += part + '/'
            if folder not in self.directories:
                if dry_run:
                    return None
                self.directories.add(folder)
                self.writes.append(('mkdir', folder))
        return folder

    def download(self, remote, destination):
        if remote == self.fail_download:
            raise OSError('Connection interrupted')
        data = self.files[remote]
        destination.write(data)
        return len(data), hashlib.sha256(data).hexdigest()

    def upload(self, folder, path, size):
        remote = folder + path.name
        if remote in self.files:
            raise AssertionError('Upload must use a unique staging name')
        self.files[remote] = b'bad upload' if self.corrupt_upload else path.read_bytes()
        self.writes.append(('upload', remote))

    def move(self, old, new):
        if self.fail_move and self.fail_move(old, new):
            raise OSError('Rename interrupted')
        if new in self.files:
            raise AssertionError('Rename must not overwrite a file')
        self.files[new] = self.files.pop(old)
        self.writes.append(('move', old, new))


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.profile = self.root / 'setup'
        self.server = MemoryServer({
            '/config/retroarch.cfg': b'video_shader_enable = "true"\n',
            '/config/Nestopia/nes.cfg': b'input_overlay = "~/Library/Caches/RetroArch/overlays/bezel.cfg"\n',
            '/config/remaps/Nestopia/nes.rmp': b'input_player1_btn_a = "1"\n',
            '/downloads/ROMS/nes/Mickey #1.zip': b'rom',
            '/playlists/Nintendo.lpl': b'{"items": []}',
            '/thumbnails/Nintendo/Named_Boxarts/Mickey.png': b'boxart',
            '/overlays/bezel.cfg': b'overlay0_overlay = "bezel.png"\n',
            '/overlays/bezel.png': b'bezel',
            '/shaders/crt.slang': b'shader source',
            '/saves/Mickey.srm': b'save',
        })
        self.server.ensure_folder(('system', 'empty'), False)
        self.server.writes.clear()
        self.output = redirect_stdout(io.StringIO())
        self.output.__enter__()
        self.addCleanup(self.output.__exit__, None, None, None)

    def capture(self, **kwargs):
        backup(self.server, self.profile, **kwargs)
        return load_profile(self.profile)

    def test_full_snapshot_and_repeat_restore(self):
        manifest = self.capture()
        self.assertNotIn('saves/Mickey.srm', manifest['files'])
        self.assertIn('system/empty', manifest['directories'])
        target = MemoryServer({'/config/retroarch.cfg': b'default'})
        restore(target, self.profile)
        for name in manifest['files']:
            self.assertEqual(target.files['/' + name], self.server.files['/' + name])
        backups = [p for p in target.files if '.restore-backup-' in p]
        self.assertEqual(len(backups), 1)
        self.assertEqual(target.files[backups[0]], b'default')
        moves = [write for write in target.writes if write[0] == 'move']
        self.assertEqual(moves[-1][2], '/config/retroarch.cfg')
        self.assertIn('/system/empty/', target.directories)
        target.writes.clear()
        restore(target, self.profile)
        self.assertEqual(target.writes, [])

    def test_both_dry_runs_are_read_only(self):
        backup(self.server, self.profile, dry_run=True)
        self.assertFalse(self.profile.exists())
        self.assertEqual(self.server.writes, [])
        self.capture()
        target = MemoryServer({'/config/retroarch.cfg': b'old'})
        restore(target, self.profile, dry_run=True)
        self.assertEqual(target.writes, [])
        self.assertEqual(target.files, {'/config/retroarch.cfg': b'old'})

    def test_failed_backup_preserves_complete_profile_and_previous(self):
        self.capture()
        self.server.files['/config/retroarch.cfg'] = b'new'
        self.capture()
        previous = self.profile.with_name('setup.previous')
        self.assertEqual((previous / 'files/config/retroarch.cfg').read_bytes(),
                         b'video_shader_enable = "true"\n')
        self.server.fail_download = '/downloads/ROMS/nes/Mickey #1.zip'
        with self.assertRaises(OSError):
            self.capture()
        self.assertEqual((self.profile / 'files/config/retroarch.cfg').read_bytes(), b'new')
        load_profile(previous)
        load_profile(self.profile)

    def test_same_size_changed_remote_is_replaced_and_backed_up(self):
        self.capture()
        target = MemoryServer({'/downloads/ROMS/nes/Mickey #1.zip': b'BAD'})
        restore(target, self.profile)
        self.assertEqual(target.files['/downloads/ROMS/nes/Mickey #1.zip'], b'rom')
        self.assertIn(b'BAD', [data for path, data in target.files.items() if '.restore-backup-' in path])

    def test_configuration_change_during_capture_preserves_previous_profile(self):
        self.capture()
        previous_manifest = (self.profile / 'manifest.json').read_bytes()
        download = self.server.download
        config_copied = Event()

        def change_config(remote, destination):
            result = download(remote, destination)
            if remote == '/config/retroarch.cfg':
                config_copied.set()
            if remote == '/thumbnails/Nintendo/Named_Boxarts/Mickey.png':
                if not config_copied.wait(timeout=5):
                    raise AssertionError('Config was not copied')
                self.server.files['/config/retroarch.cfg'] = b'new settings'
            return result

        self.server.download = change_config
        with self.assertRaisesRegex(ValueError, 'configuration changed during backup'):
            self.capture()
        self.assertEqual((self.profile / 'manifest.json').read_bytes(), previous_manifest)

    def test_changing_history_is_retried_with_fresh_size(self):
        remote = '/playlists/builtin/content_history.lpl'
        self.server.ensure_folder(('playlists', 'builtin'), False)
        self.server.files[remote] = b'old'
        download = self.server.download
        calls = []

        def change_history(path, destination):
            if path == remote:
                calls.append(path)
                self.server.files[path] = b'updated history'
            return download(path, destination)

        self.server.download = change_history
        self.capture()
        self.assertEqual(len(calls), 2)
        self.assertEqual((self.profile / ('files' + remote)).read_bytes(), b'updated history')

    def test_truncated_download_is_rejected_after_retry(self):
        self.capture()
        previous_manifest = (self.profile / 'manifest.json').read_bytes()
        download = self.server.download

        def truncated(path, destination):
            if path.endswith('.zip'):
                destination.write(b'r')
                return 1, hashlib.sha256(b'r').hexdigest()
            return download(path, destination)

        self.server.download = truncated
        with self.assertRaisesRegex(ValueError, 'download was truncated'):
            self.capture()
        self.assertEqual((self.profile / 'manifest.json').read_bytes(), previous_manifest)

    def test_modified_profile_fails_before_remote_access(self):
        self.capture()
        (self.profile / 'files/downloads/ROMS/nes/Mickey #1.zip').write_bytes(b'BAD')
        with patch.object(self.server, 'ensure_folder', side_effect=AssertionError('Remote accessed')):
            with self.assertRaisesRegex(ValueError, 'hash or size mismatch'):
                restore(self.server, self.profile)

    def test_path_traversal_and_symlink_are_refused(self):
        self.capture()
        manifest_path = self.profile / 'manifest.json'
        original = manifest_path.read_text()
        manifest = json.loads(original)
        manifest['files']['../escape'] = {'bytes': 0, 'sha256': ''}
        manifest_path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, 'Unsafe profile path'):
            load_profile(self.profile)
        manifest_path.write_text(original)
        cfg = self.profile / 'files/config/retroarch.cfg'
        outside = self.root / 'outside.cfg'
        cfg.rename(outside)
        cfg.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, 'Symlinks'):
            load_profile(self.profile)

    def test_local_source_and_apple_uuid_portability(self):
        source = self.root / 'local'
        source.mkdir()
        home = '/private/var/mobile/Containers/Data/Application/01234567-89AB-CDEF-0123-456789ABCDEF'
        bundle = '/private/var/containers/Bundle/Application/01234567-89AB-CDEF-0123-456789ABCDEF/RetroArch.app'
        (source / 'retroarch.cfg').write_text(f'thumbnails_directory = "{home}/Library/Caches/RetroArch/thumbnails"\n')
        playlists = source / 'playlists'
        playlists.mkdir()
        (playlists / 'test.lpl').write_text(json.dumps({'core_path': bundle + '/Frameworks/nestopia.libretro.framework'}))
        backup(None, self.profile, source=source)
        load_profile(self.profile)
        self.assertIn('~/Library/Caches/RetroArch/thumbnails', (self.profile / 'files/config/retroarch.cfg').read_text())
        self.assertIn(':/Frameworks/', (self.profile / 'files/playlists/test.lpl').read_text())
        self.assertIn(home, (source / 'retroarch.cfg').read_text())

    def test_opt_in_saves(self):
        self.assertIn('saves/Mickey.srm', self.capture(include_saves=True)['files'])

    def test_bad_staged_upload_keeps_original(self):
        self.capture()
        target = MemoryServer({'/config/retroarch.cfg': b'original'})
        target.corrupt_upload = True
        with self.assertRaisesRegex(ValueError, 'Staged upload verification failed'):
            restore(target, self.profile)
        self.assertEqual(target.files['/config/retroarch.cfg'], b'original')
        self.assertFalse(any(w[0] == 'move' for w in target.writes))

    def test_failed_activation_restores_previous_file(self):
        self.capture()
        target = MemoryServer({'/config/retroarch.cfg': b'original'})
        target.fail_move = lambda old, new: '/restore-' in old and new == '/config/retroarch.cfg'
        with self.assertRaisesRegex(OSError, 'Rename interrupted'):
            restore(target, self.profile)
        self.assertEqual(target.files['/config/retroarch.cfg'], b'original')
        target.fail_move = None
        restore(target, self.profile)
        self.assertEqual(target.files['/config/retroarch.cfg'], self.server.files['/config/retroarch.cfg'])

    def test_directory_conflict_stops_before_any_writes(self):
        self.capture()
        target = MemoryServer({'/downloads': b'conflict'})
        with self.assertRaisesRegex(ValueError, 'not a directory'):
            restore(target, self.profile)
        self.assertEqual(target.writes, [])

    def test_backup_verification_failure_rolls_back_original(self):
        self.capture()
        target = MemoryServer({'/config/retroarch.cfg': b'original'})
        download = target.download

        def fail_backup_download(remote, destination):
            if '.restore-backup-' in remote:
                raise OSError('Backup download interrupted')
            return download(remote, destination)

        target.download = fail_backup_download
        with self.assertRaisesRegex(OSError, 'Backup download interrupted'):
            restore(target, self.profile)
        self.assertEqual(target.files['/config/retroarch.cfg'], b'original')


if __name__ == '__main__':
    unittest.main()
