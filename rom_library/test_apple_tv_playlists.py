"""Check playlist generation without local device state or ROM assets."""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import apple_tv_playlists as playlists


class PlaylistTests(unittest.TestCase):
    def test_fresh_checkout_has_explicit_apple_tv_core_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            export, output = root / 'export', root / 'playlists'
            for console in playlists.NAMES:
                folder = export / console
                folder.mkdir(parents=True)
                (folder / 'selection.json').write_text(json.dumps([
                    {'title': 'Test game', 'file': 'Test game.zip', 'archive_member': 'Test game.nes'}]))
            original_exists = Path.exists
            # Exercise the tracked fallback even in the original populated workspace.
            def exists(path):
                return False if path.name == 'apple-tv-core-defaults.json' else original_exists(path)
            with patch('sys.argv', ['playlists', '--export', str(export), '--output', str(output)]), \
                    patch.object(Path, 'exists', exists), redirect_stdout(io.StringIO()):
                playlists.main()
            for console, system in playlists.NAMES.items():
                data = json.loads((output / (system + '.lpl')).read_text())
                self.assertTrue(data['default_core_path'].startswith(':/Frameworks/'))
                self.assertTrue(data['default_core_path'].endswith('.framework'))
                item = data['items'][0]
                self.assertEqual(item['core_path'], data['default_core_path'])
                self.assertEqual(item['path'],
                    f'~/Library/Caches/RetroArch/downloads/ROMS/{console}/Test game.zip#Test game.nes')


if __name__ == '__main__':
    unittest.main()
