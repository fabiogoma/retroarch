"""Project settings: config.json defaults, overridden by an optional untracked config.local.json."""
import json
from pathlib import Path

BASE = Path(__file__).resolve().parent


def load():
    settings = json.loads((BASE / 'config.json').read_text())
    local = BASE / 'config.local.json'
    if local.is_file():
        override = json.loads(local.read_text())
        unknown = set(override) - set(settings)
        if unknown:
            raise ValueError(f'Unknown settings in config.local.json: {sorted(unknown)}')
        settings.update(override)
    return settings


CONFIG = load()
