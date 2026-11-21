"""Content-free tests for verified downloads, safe paths and repeat runs."""
from contextlib import redirect_stdout, redirect_stderr
import hashlib
import io
import json
import ssl
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

import download_assets as assets


class DownloadTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'content'
        self.data = b'content-free test data'
        self.files = [{'name': 'nested/Game #1.zip', 'size': str(len(self.data)),
                       'sha1': hashlib.sha1(self.data).hexdigest()}]
        self.manifest = {'version': 1, 'files': [
            {'item': 'test-item', 'file': self.files[0]['name'],
             'destination': 'nes/archive/Game.zip'}]}
        self.requests = []
        self.patcher = patch.object(assets, 'request', side_effect=self.request)
        self.patcher.start()
        self.addCleanup(self.patcher.stop)
        output = redirect_stdout(io.StringIO())
        output.__enter__()
        self.addCleanup(output.__exit__, None, None, None)

    def request(self, url):
        self.requests.append(url)
        if '/metadata/' in url:
            return io.BytesIO(json.dumps({'files': self.files}).encode())
        return io.BytesIO(self.data)

    def plan(self):
        return assets.archive_plan(self.manifest)

    def test_download_verifies_bytes_and_rerun_skips_network(self):
        plan = self.plan()
        self.assertTrue(plan[0]['url'].endswith('/nested/Game%20%231.zip'))
        assets.download(plan, self.root)
        self.assertEqual((self.root / 'nes/archive/Game.zip').read_bytes(), self.data)
        self.requests.clear()
        assets.download(plan, self.root)
        self.assertEqual(self.requests, [])

    def test_artwork_uses_explicit_playlist_label_destination(self):
        self.manifest['files'][0]['destination'] = 'artwork/System/Named_Boxarts/Curated Label.png'
        assets.download(self.plan(), self.root)
        self.assertEqual((self.root / self.manifest['files'][0]['destination']).read_bytes(), self.data)

    def test_dry_run_reads_metadata_but_writes_nothing(self):
        assets.download(self.plan(), self.root, dry_run=True)
        self.assertFalse(self.root.exists())
        self.assertEqual(len(self.requests), 1)

    def test_checksum_or_size_mismatch_leaves_no_target_or_partial_file(self):
        for size, sha in [(len(self.data), '0' * 40), (len(self.data) - 1, self.files[0]['sha1']),
                          (len(self.data) + 1, self.files[0]['sha1'])]:
            with self.subTest(size=size, sha=sha):
                self.files[0].update(size=str(size), sha1=sha)
                with self.assertRaises(ValueError):
                    assets.download(self.plan(), self.root)
                self.assertEqual(list(self.root.rglob('*.*')), [])

    def test_existing_different_file_preserved_before_any_download(self):
        path = self.root / 'nes/archive/Game.zip'
        path.parent.mkdir(parents=True)
        path.write_bytes(b'original')
        plan = self.plan()
        self.requests.clear()
        with self.assertRaises(ValueError):
            assets.download(plan, self.root)
        self.assertEqual(path.read_bytes(), b'original')
        self.assertEqual(self.requests, [])

    def test_unsafe_paths_symlinks_and_duplicate_destinations_refused(self):
        plan = self.plan()
        for name in ('../outside', '/absolute', 'nes/../outside', 'nes\\file',
                     'nes//file', 'nes/./file', 'rom_library/roms.py', '.github/a', 'nes/a\n'):
            with self.subTest(name=name), self.assertRaises(ValueError):
                assets.download([{**plan[0], 'destination': name}], self.root)
        self.assertFalse(self.root.exists())
        with self.assertRaises(ValueError):
            assets.download([plan[0], {**plan[0], 'destination': 'nes/archive/game.zip'}], self.root)
        self.root.mkdir()
        (self.root / 'nes').symlink_to(Path(self.temp.name), target_is_directory=True)
        with self.assertRaises(ValueError):
            assets.download(plan, self.root)
        self.assertFalse((Path(self.temp.name) / 'archive').exists())

    def test_missing_item_file_checksum_or_pin_rejected(self):
        with patch.object(assets, 'metadata', return_value=[]), self.assertRaises(ValueError):
            self.plan()
        del self.files[0]['sha1']
        with self.assertRaises(ValueError):
            self.plan()
        self.manifest['files'][0]['sha256'] = '0' * 64
        with self.assertRaises(ValueError):
            assets.download(self.plan(), self.root)
        with patch.object(assets, 'request', return_value=io.BytesIO(b'[]')):
            with self.assertRaises(ValueError):
                assets.metadata('missing')

    def test_access_denied_stops_before_requesting_other_content(self):
        plan = self.plan()
        plan.append({**plan[0], 'destination': 'snes/another.zip'})
        error = HTTPError(plan[0]['url'], 403, 'Forbidden', {}, None)
        with patch.object(assets, 'request', side_effect=error) as request:
            with self.assertRaises(HTTPError):
                assets.download(plan, self.root)
            self.assertEqual(request.call_count, 1)
        self.assertEqual(list(self.root.rglob('*.*')), [])
        with patch.object(assets, 'metadata', side_effect=error), redirect_stderr(io.StringIO()):
            self.assertEqual(assets.main(['--list', 'test-item']), 1)

    def test_interrupted_stream_cleans_up_and_rerun_succeeds(self):
        class Interrupted(io.BytesIO):
            def read(self, size=-1):
                if self.tell():
                    raise OSError('Connection interrupted')
                return super().read(4)
        plan = self.plan()
        with patch.object(assets, 'request', return_value=Interrupted(self.data)):
            with self.assertRaises(OSError):
                assets.download(plan, self.root)
        self.assertEqual(list(self.root.rglob('*.*')), [])
        assets.download(plan, self.root)
        self.assertEqual((self.root / 'nes/archive/Game.zip').read_bytes(), self.data)

    def test_pinned_bezels_are_only_fetched_on_request(self):
        plan = assets.bezel_plan()
        self.assertEqual(len(plan), 6)
        self.assertEqual(self.requests, [])
        self.assertTrue(all(p['destination'].startswith('rom_library/bezels/') for p in plan))
        self.assertTrue(all(len(p['hashes']['sha256']) == 64 for p in plan))

    def test_https_request_keeps_certificate_and_hostname_verification(self):
        with patch.object(assets, 'urlopen') as opener:
            # Bypass the test's transport double to inspect the real HTTPS setup.
            self.patcher.stop()
            try:
                assets.request('https://archive.org/metadata/test-item')
            finally:
                self.patcher.start()
            context = opener.call_args.kwargs['context']
            self.assertEqual(context.verify_mode, ssl.CERT_REQUIRED)
            self.assertTrue(context.check_hostname)


if __name__ == '__main__':
    unittest.main()
