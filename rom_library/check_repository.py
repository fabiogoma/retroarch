"""Reject tracked runtime data and binary assets before publishing."""
from pathlib import Path
import subprocess
import sys


def main():
    root = Path(__file__).resolve().parent.parent
    paths = subprocess.check_output(['git', 'ls-files', '-z'], cwd=root).decode().split('\0')
    errors = []
    root_files = {'.gitignore', 'README.md', 'COPILOT_HANDOFF.md', 'RESUME_WITH_COPILOT.md'}
    library_files = {'README.md', 'consoles.json', 'curation.json', 'apple_tv_cores.json',
                     'archive_sources.example.json'}
    for name in filter(None, paths):
        path = Path(name)
        parts = path.parts
        allowed = name in root_files
        if parts[0] == 'rom_library':
            allowed = (len(parts) == 2 and (path.suffix == '.py' or path.name in library_files)
                       or name in {'rom_library/bezels/README.md', 'rom_library/bezels/LICENSE',
                                   'rom_library/bezels/sources.json'}
                       or len(parts) >= 3 and parts[1] == 'bezels' and path.suffix == '.cfg')
        elif parts[0] == 'docs':
            allowed = name in {'docs/OPERATIONS.md', 'docs/DOWNLOADS.md'}
        elif parts[0] == 'config':
            allowed = name in {'config/apple-tv.cfg', 'config/global.slangp',
                               'config/pce-six-button.opt'}
        elif parts[0] == '.github':
            allowed = name in {'.github/copilot-instructions.md', '.github/workflows/checks.yml'}
        if not allowed:
            errors.append(name)
            continue
        try:
            text = (root / name).read_text(encoding='utf-8')
            if '\0' in text or (root / name).is_symlink():
                errors.append(name)
        except (OSError, UnicodeError):
            errors.append(name)
    if errors:
        print('Refusing runtime data, assets, or unexpected files:', *errors, sep='\n', file=sys.stderr)
        return 1
    print(f'Checked {len(list(filter(None, paths)))} tracked source/configuration files; no content assets.')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
