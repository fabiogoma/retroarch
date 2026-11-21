"""Exercise repeat syncs and remote conflicts using a server double."""
import hashlib
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from sync_apple_tv import RetroArchServer, sync


class SyncTests(unittest.TestCase):
    def test_http_download_streams_encoded_path_and_computes_hash(self):
        data = b'x' * (1024 * 1024 + 123)
        server = RetroArchServer('http://apple-tv.test/')
        destination = io.BytesIO()
        with patch('sync_apple_tv.urlopen', return_value=io.BytesIO(data)) as request:
            self.assertEqual(server.download('/downloads/ROMS/A #1.zip', destination),
                             (len(data), hashlib.sha256(data).hexdigest()))
        self.assertEqual(destination.getvalue(), data)
        self.assertIn('path=%2Fdownloads%2FROMS%2FA+%231.zip', request.call_args.args[0])

    def test_http_listing_rejects_unsafe_and_duplicate_entries(self):
        import json
        server = RetroArchServer('http://apple-tv.test')
        bad_entries = [
            [{'name': '../escape', 'path': '/config/../escape', 'size': 1}],
            [{'name': 'file', 'path': '/other/file', 'size': 1}],
            [{'name': 'file', 'path': '/config/file', 'size': 1}] * 2,
        ]
        for entries in bad_entries:
            with self.subTest(entries=entries):
                with patch('sync_apple_tv.urlopen', return_value=io.BytesIO(json.dumps(entries).encode())):
                    with self.assertRaisesRegex(ValueError, 'Invalid remote listing'):
                        server.list('/config/')

    def test_http_move_uses_server_rename_protocol(self):
        server = RetroArchServer('http://apple-tv.test')
        with patch('sync_apple_tv.urlopen', return_value=io.BytesIO()) as request:
            server.move('/config/old.cfg', '/config/new.cfg')
        self.assertEqual(request.call_args.args[0], 'http://apple-tv.test/move')
        self.assertEqual(request.call_args.kwargs['data'], b'oldPath=%2Fconfig%2Fold.cfg&newPath=%2Fconfig%2Fnew.cfg')

    def test_repeat_sync_uploads_only_missing_files(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'Mickey Mousecapade.nes'
            path.write_bytes(b'rom')
            entry = {'bytes': 3, 'export_sha256': hashlib.sha256(b'rom').hexdigest()}
            groups = {'nes': {path.name: (path, entry)}}
            remote = {}
            server = Mock()
            server.ensure_folder.return_value = '/downloads/ROMS/nes/'
            server.list.return_value = remote
            server.upload.side_effect = lambda folder, path, size: remote.update(
                {path.name: {'size': size}}
            )

            sync(server, groups, dry_run=True)
            server.upload.assert_not_called()
            sync(server, groups, dry_run=False)
            sync(server, groups, dry_run=False)
            server.upload.assert_called_once_with('/downloads/ROMS/nes/', path, 3)

            remote[path.name]['size'] = 2
            with self.assertRaisesRegex(ValueError, 'different size or type'):
                sync(server, groups, dry_run=False)
            self.assertEqual(server.upload.call_count, 1)
