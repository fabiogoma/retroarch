# ROM catalog and family selection

See the [repository README](../README.md) for the fresh-checkout setup flow and
[download configuration](../docs/DOWNLOADS.md) for ROMs/artwork from Internet Archive.
Git contains no ROMs, artwork, root device config, generated state or complete
profile. Those remain local. Fetch pinned console PNGs with
`python3 -B rom_library/download_assets.py --bezels` before running the bezel installer.
The historical local records mentioned below are unavailable in a fresh clone.

From the ROMs root, run:

```sh
uv run --no-project rom_library/roms.py
```

Python 3.11+ and uv are required; there are no Python package dependencies. Archive support uses the system libarchive (included on this Mac; install the OS libarchive package on Linux). Cataloging and playlist generation work offline. The ROM sync and optional Master System downloader use the network. In a restricted environment, use `UV_CACHE_DIR=/private/tmp/roms-uv-cache uv run --offline --no-project rom_library/roms.py`.

The script rescans every console folder recursively, refreshes the SQLite catalog, and rebuilds `AppleTV-Top40` with copies of the selected games. Original files are never moved or deleted. The previous generated export is retained at `rom_library/state/previous-export`; the next successful run replaces that backup. Do not put personal files inside either generated export directory. Managed extraction caches remain in `rom_library/state/extracted`; originals may already have identical extracted copies, and the catalog preserves those source records while export chooses one variant per curated title. SHA-256 hashes identify the actual ROM bytes, including inside ZIPs. This checks file consistency, not authenticity against a preservation DAT.

## Outputs

- `../AppleTV-Top40/<console>/`: the ROMs to upload, plus `TOP40.md` and `selection.json` explaining every selection, multiplayer mode, family notes, source file, hashes, language inference and popularity evidence.
- `../AppleTV-Top40/summary.json`: counts, unavailable curated games, incomplete downloads and archive problems.
- `state/catalog.sqlite`: one catalog across all consoles. `files` inventories physical inputs, `archives` records every archive member and extraction decision, `games` records playable file variants, `titles` is the canonical normalized-title view, and `selections` records this export's choices. `issues` records failed archive reads. Game IDs are regenerated per scan; use `(console, title_key)` or SHA-256 as durable references.
- `state/catalog.csv`: all playable source variants; `state/titles.csv`: normalized title catalog. Different regional titles and compilations can still have different normalized identities. This is filename-based identification, not an authoritative game database match.
- `curation.json`: editable ordered recommendations, aliases, genre, family notes, language allowance and evidence. Order expresses personal suitability, not a fabricated sales ranking.

## Archive policy

A ZIP containing exactly one recognized cartridge ROM stays compressed. Collection bundles, ZIPs with multiple files, and RAR/7z archives are extracted into a managed cache; nested archives are processed recursively. Unsupported compression is therefore not sent to Apple TV. ZIP loading is supported by RetroArch, but exact behavior depends on the selected core/build. To generate entirely uncompressed exports if needed:

```sh
uv run --no-project rom_library/roms.py --unzip-export
```

The reader explicitly selects the outer RAR/7z/ZIP format: unrestricted libarchive format detection mistakes this PC Engine RAR's embedded ZIP data for a standalone ZIP. Paths are validated before extraction, links/special files are refused, nesting is limited, and extracted data is limited to 32 GiB per archive. Broken archives are reported, not silently counted as games. Incomplete `.part`, `.download` and `.crdownload` files never enter the selection.

## Selection policy and future additions

Add complete games anywhere inside a console folder and rerun. The script searches the ordered curation list, chooses up to 40 available titles, prefers USA, then World, then Europe variants, and avoids filename-marked prototypes, samples, hacks and bad dumps. Japanese releases are allowed for reviewed PC Engine and Mickey entries that explicitly opt in and are marked in manifests; menu/text assistance may be needed. Prefer English adventure releases when acquiring new games.

Mickey games are placed first in each reviewed console list so available releases make the cut. This includes starring roles, the Disneyland game Mickey hosts, the Genesis compilation containing Castle of Illusion, and the preserved SNES Playtown prototype. Known Mickey titles missing from the local collection appear in the export summary. The PC Engine list has no matching Mickey release in the local library. Use `--count N` to generate a separate `AppleTV-TopN` export.

The recommendations combine published million-seller lists with Disney, Marvel/DC, real-time adventure, beat-em-up, arcade racing (especially Top Gear and Mario Kart), and family preferences. The user-authorized Super Mario RPG is the only turn-based RPG exception. Other turn-based RPGs, including the existing Chrono Trigger and Breath of Fire II, remain in the full catalog. Family notes are editorial guidance, not verified age ratings or a promise that any game suits a two-year-old. Cooperative, alternating-turn and competitive modes are differentiated; technical difficulty and spooky/combat content are called out. Individual play modes have not been tested on your Apple TV.

New folders with recognized cartridge extensions are detected automatically, including GB/GBC/GBA. For a new console, add its extensions and folder aliases to `consoles.json` if absent, and add an ordered reviewed list under its canonical name in `curation.json`. Copy the entry schema from an existing list and add aliases for differing filenames. The tool intentionally reports missing curation instead of inventing a reliable family ranking for an unknown console. Arbitrary future games outside the reviewed list are cataloged but do not automatically displace reviewed choices. Disc games (CUE/BIN sets, CHD, multi-disc playlists), arcade ZIP sets, and BIOS handling need a dedicated console profile implementation; do not treat them as cartridge bundles.

Example read-only SQL:

```sql
SELECT console, COUNT(*) AS titles FROM titles GROUP BY console;
SELECT console, title, variants, distinct_dumps FROM titles ORDER BY console, title;
SELECT console, rank, title FROM selections ORDER BY console, rank;
SELECT * FROM issues;
```

## Master System source

The supplied source is https://archive.org/download/master_system . The existing collection already contained 372 single-game ZIPs. The metadata endpoint was accessible, but missing-game download attempts returned HTTP 401/403; no additional games were downloaded. Details are in `state/master_system_downloads.json` and the metadata snapshot in `state/master_system_metadata.json`. Once the source permits access, the optional command below downloads titles absent from the current Master System collection, validates advertised size/SHA-1 and ZIP CRC, and does not overwrite existing title files:

```sh
uv run --no-project rom_library/download_master_system.py
uv run --no-project rom_library/roms.py
```

The downloader stops on authorization failures. It has no authentication-bypass behavior. Alternate regional/renamed titles can differ in the source's filenames; comparison uses normalized names.

## Apple TV

### Save and restore the complete setup

Capture the current Apple TV once, with RetroArch open and its web file server running:

```sh
uv run --no-project rom_library/sync_apple_tv.py --backup
```

The reusable copy lives in `rom_library/retroarch-profile/`. It contains `manifest.json`
and a `files/` tree with the main configuration, core/game options and controller
remaps, playlists, ROMs under `downloads/ROMS`, thumbnails, overlays/bezels, shaders,
controller autoconfigs, menu assets, and BIOS/system files. Missing optional folders
are skipped. ROMs are captured from the device, independently of the curated export.
Save your desired settings on the device first, then leave RetroArch idle during
the capture. Changes still held only in memory cannot be backed up by the file server.
The capture only reads from the Apple TV.
It uses four parallel downloads and retries a file once if its size changes after
inventory (for example, a history playlist). If the main configuration changes
during capture, the snapshot is rejected so you can retry with RetroArch idle.

On a new Apple TV, install and open RetroArch, start its web file server, then run:

```sh
uv run --no-project rom_library/sync_apple_tv.py --restore --server http://NEW-APPLE-TV-IP
```

Fully restart RetroArch afterward to load the restored settings. Avoid saving the
old in-memory settings over the restored configuration before restarting. Controller
Bluetooth pairing and Apple TV display/audio settings still need to be set on the
device. The script transfers data; RetroArch and its compatible cores must already
be installed. It preserves existing playlist labels and core choices, so no manual
ROM scan is needed for the captured playlists.

Both commands support `--dry-run` (no profile or server changes), `--server URL`,
and `--profile PATH`. Backups are checked with SHA-256 and staged locally before
activation; the last complete profile is retained as `retroarch-profile.previous`.
An interrupted backup leaves the existing profile intact. Keep the entire profile
folder together when copying it elsewhere. Treat `files/` and `manifest.json` as a
snapshot: editing the files invalidates its hashes. Rerun `--backup` after changing
the setup on the device.

Restore validates every local file before writing to the server, skips identical
files by hash (including on reruns), and uploads dependencies before playlists and
the main configuration. Replaced device files remain beside their originals as
`<filename>.restore-backup-<run-id>`. Each replacement is uploaded with a unique
temporary name, downloaded for verification, and activated through the server's
rename endpoint. If activation fails with the target missing, the previous file is
restored automatically. An interrupted restore can be rerun; temporary files or
preserved backups may remain on the device. Unrelated files are kept. The operation
is resumable file by file, rather than an atomic transaction across the entire device.

Game saves and save states are optional; include them when capturing if wanted:

```sh
uv run --no-project rom_library/sync_apple_tv.py --backup --include-saves
```

You can also capture an already copied RetroArch data folder with
`--backup --source /path/to/RetroArch`. It must use the same directory layout;
the main config can be either `config/retroarch.cfg` or `retroarch.cfg` at the root.
Apple installation UUIDs in configuration/preset/playlist files are converted to
RetroArch's `~` and `:` aliases for reuse on another Apple TV. Desktop-specific
paths and core choices require manual adaptation before capture; this is an Apple TV
restore tool, not a cross-platform configuration converter. Symlinks and unsafe
paths are refused.

The default endpoint remains `http://192.168.0.249`; use the address and port shown
by your device. This tool uses the web upload server (`/list`, `/download`, `/upload`,
`/move`), rather than WebDAV. [Libretro's Apple installation guide](https://docs.libretro.com/guides/install-ios/)
explains the file servers and tvOS cache behavior. Keeping this copy outside the
Apple TV lets you recover when its cached files are removed.

### Upload only the curated ROM export

Keep RetroArch open and its web file server running. From the ROMs root, preview or sync the generated set:

```sh
uv run --no-project rom_library/sync_apple_tv.py --dry-run
uv run --no-project rom_library/sync_apple_tv.py
```

The sync defaults to `http://192.168.0.249` and uploads manifest-listed ROMs to `/downloads/ROMS/<console>/`. Use `--server` for a different device or `--export` for a different generated set. Existing files with the same name and byte size are skipped. A same-name file with a different size stops the sync for review. The server listing supplies sizes but no hashes for existing files. Each new file is checked against the local manifest hash before upload and verified by name and size afterward. Rerun after an interrupted transfer to send the remaining files. Remote ROMs, playlists and thumbnails are kept in place.

To add newly uploaded ROMs to RetroArch’s menus, select a suitable core, then manually scan each console folder with its correct system name. Enable archive scanning for single-game ZIPs. The upload folder README includes suggested cores. Import on the device rather than using desktop-path playlists. N64 performance/core availability depend on your Apple TV and RetroArch build; the games have not been play-tested on that device.

## Research references

Research checked 21 November 2026. Sales references are secondary compilations with citations; sales, shipments and console bundles are not interchangeable. No exact unit sales or complete bestseller claims are invented for games without reliable evidence. Per-game evidence is in `curation.json` and exported manifests.

- [NES million-seller list](https://en.wikipedia.org/wiki/List_of_best-selling_Nintendo_Entertainment_System_video_games)
- [SNES million-seller list](https://en.wikipedia.org/wiki/List_of_best-selling_Super_Nintendo_Entertainment_System_video_games)
- [Genesis million-seller list](https://en.wikipedia.org/wiki/List_of_best-selling_Sega_Genesis_video_games)
- [Nintendo 64 million-seller list](https://en.wikipedia.org/wiki/List_of_best-selling_Nintendo_64_video_games)
- [Nintendo's SNES Classic selection](https://en-americas-support.nintendo.com/app/answers/detail/a_id/26387/)
- [Time Extension: Master System favorites](https://www.timeextension.com/guides/best-sega-master-system-games-of-all-time)
- [Time Extension: PC Engine favorites](https://www.timeextension.com/guides/best-pc-engine-turbografx-16-games)
- [Libretro playlists and archive paths](https://docs.libretro.com/guides/roms-playlists-thumbnails/)
- [Genesis Plus GX format support](https://docs.libretro.com/library/genesis_plus_gx/)
- [Libretro Apple platform installation and JIT constraints](https://docs.libretro.com/guides/install-ios/)

## Verification

```sh
uv run --no-project python -m unittest discover -s rom_library -p 'test_*.py' -v
```

Tests cover nested bundles, single-ROM ZIP preservation, unsafe paths, preferred-region selection, stale-catalog refresh, uncompressed export, preserving the previous set after a failed export, and the PC Engine RAR format-detection regression. Final real exports are also checked against manifest hashes and ROM byte hashes.

Snapshot tests also cover complete capture/restore, read-only previews, repeat runs,
same-size remote changes, corrupted uploads, interrupted captures and activation,
profile tampering, symlinks/path traversal, portable Apple paths and optional saves.

The racing preference update promotes Top Gear and Top Gear 2 on SNES, Top Gear 2/OutRun/Micro Machines 2 on Genesis, Excitebike/R.C. Pro-Am II on NES, OutRun on Master System, and existing arcade racers on PC Engine. The N64 list now includes its completed collection and a wider selection of kart, road and futuristic racers. Availability and exact selected titles are recorded in the current export manifests.

## Apple TV playlist repair

`apple_tv_playlists.py` generates direct playlists for the uploaded `downloads/ROMS/<console>` layout. It defaults to Apple RetroArch’s `~` home alias and uses `.framework` bundle paths from `apple_tv_cores.json`, with local `state/apple-tv-core-defaults.json` taking precedence when present. Do not append the framework executable name: that changes the core identifier used for matching. Regenerate with `uv run --no-project rom_library/apple_tv_playlists.py`. Upload the resulting `.lpl` files to the device playlist directory, preserving existing originals first.

The load-error investigation is recorded in `state/load-error-investigation/repair.json`. Original remote files were backed up locally and on the device before replacement. Debug file logging was enabled for the next on-device test; turn Log to File and Logging Verbosity off after the issue is resolved. Playback has not been confirmed remotely.

## Configuration

`rom_library/config.json` holds the settings: RetroArch server URL, games per console, export folder prefix, device home alias, remote ROM root, timeouts, download size cap and user agent. Put personal overrides in the untracked `rom_library/config.local.json` (same keys; unknown keys are rejected). Command-line flags still override both.
