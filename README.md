# RetroArch on Apple TV

Python tools and configuration for preparing a curated cartridge library and
managing RetroArch on Apple TV from a Mac or Linux host. The current curation
targets 40 games each for NES, SNES, Nintendo 64, Genesis, Master System and
PC Engine, with family/multiplayer notes and explicit core choices.

The repository contains **source code, curation, configuration and documentation**.
ROMs, BIOS files, artwork, shaders downloaded from upstream, device profiles,
exports, saves and local state stay outside Git. `.gitignore` uses a source
allowlist, and CI rejects tracked content assets. Your existing local content is
kept in place.

## Requirements

- Python 3.11+ on macOS/Linux; no third-party Python packages.
- System libarchive for archive scanning (macOS includes it; Linux needs its
  libarchive package, such as `libarchive-dev` on Ubuntu).
- RetroArch installed on Apple TV with the appropriate cores and its web file
  server running. Use the address displayed by your device with `--server`.
- Internet access for downloads; cataloging/exporting work offline afterward.

These tools transfer data and settings. Install RetroArch itself on Apple TV
separately; see [Libretro's Apple installation guide](https://docs.libretro.com/guides/install-ios/).
Set Apple TV output to 60 Hz for the existing NTSC-oriented setup. Core availability
and N64 performance depend on your RetroArch build/device.

## Start from a fresh checkout

```sh
git clone https://github.com/fabiogoma/retroarch.git
cd retroarch
python3 -B -m unittest discover -s rom_library -p 'test_*.py' -v
```

1. **Choose content sources.** Copy the example download manifest, add explicit
   Internet Archive item/file mappings for ROMs and artwork, then preview it:

   ```sh
   cp rom_library/archive_sources.example.json archive_sources.local.json
   python3 -B rom_library/download_assets.py --manifest archive_sources.local.json --dry-run
   python3 -B rom_library/download_assets.py --manifest archive_sources.local.json
   ```

   The empty example deliberately requires source configuration. Only one Archive
   source was supplied in the original project (`master_system`); its previous
   file requests returned 401/403. Complete, working sources for all six consoles
   and their box art have not been established. The downloader supports both
   ROMs and artwork from Archive; it stops on access denial and verifies sizes
   and checksums. See [download configuration](docs/DOWNLOADS.md).

2. **Build the library.** Downloads go into the console folders at the checkout
   root, alongside any existing collection. Scan and create the curated export:

   ```sh
   python3 -B rom_library/roms.py
   ```

   Review `ROMs/AppleTV-Top40/summary.json` for missing games and archive issues.
   Existing originals are never moved/deleted. A full scan reads every source
   and may hydrate iCloud placeholders; it is unnecessary just to inspect an
   existing setup.

3. **Sync ROMs and generate playlists.** Keep RetroArch open and idle:

   ```sh
   python3 -B rom_library/sync_apple_tv.py --server http://APPLE-TV-IP --dry-run
   python3 -B rom_library/sync_apple_tv.py --server http://APPLE-TV-IP
   python3 -B rom_library/apple_tv_playlists.py
   ```

   ROM sync is additive. Playlist generation writes local files under
   `rom_library/state/apple-tv-playlists/` with Apple TV framework/home aliases.
   Back up current device playlists before installing these through the web file
   server; generation does not upload them. See [operations](docs/OPERATIONS.md).

4. **Configure the picture, audio and artwork.** `config/apple-tv.cfg` is a
   settings fragment to merge with a backed-up device config. `config/global.slangp`
   references CRT EasyMode, which must already be installed from RetroArch's
   upstream shader collection. `config/pce-six-button.opt` is a game-specific
   Street Fighter II fragment. These files are never applied automatically.

   Box art downloaded into `artwork/` must be installed under the matching
   `/thumbnails/<system>/Named_Boxarts/<playlist label>.png` path. The known console
   bezel PNGs can also be fetched from their original, hash-pinned Libretro source:

   ```sh
   python3 -B rom_library/download_assets.py --bezels
   python3 -B rom_library/install_console_bezels.py --server http://APPLE-TV-IP
   python3 -B rom_library/install_console_bezels.py --server http://APPLE-TV-IP --install
   ```

   Archive-hosted bezel files can instead be mapped to the same local destinations
   with the manifest; the installer requires the existing pinned image hashes.
   Check one game per console on the TV after changes.

## Back up and restore an existing setup

```sh
python3 -B rom_library/sync_apple_tv.py --backup --server http://APPLE-TV-IP
python3 -B rom_library/sync_apple_tv.py --restore --server http://NEW-APPLE-TV-IP --dry-run
python3 -B rom_library/sync_apple_tv.py --restore --server http://NEW-APPLE-TV-IP
```

The complete, hash-verified local snapshot lives in `rom_library/retroarch-profile/`
and is **ignored by Git**, including ROMs/artwork/BIOS. Store it separately if you
want disaster recovery. `--include-saves` opts into saves during backup. Restart
RetroArch after restoring without saving stale in-memory settings over the result.
Cloning this repository does not recreate an excluded device snapshot.

## Project map

- [Library tooling](rom_library/README.md): archive safety, curation and backup/restore.
- [Operations](docs/OPERATIONS.md): device paths, installation and verification.
- [Download configuration](docs/DOWNLOADS.md): Archive file mappings and integrity.
- [Historical handoff](COPILOT_HANDOFF.md): dated evidence from the original local
  installation; its ignored state files/content are unavailable in a fresh clone.

```sh
python3 -B rom_library/check_repository.py
python3 -B -m unittest discover -s rom_library -p 'test_*.py' -v
```
