# ROM library and Apple TV handoff

Repository preparation note, 21 November 2026: this document preserves the earlier
local installation snapshot. The project is now prepared for
`fabiogoma/retroarch`; see `README.md` for the current checkout/download workflow.
ROMs, artwork, device profiles, root `retroarch.cfg`, generated exports, `state/`
and `docs/handoff-snapshot.json` are deliberately ignored. References to those
files below apply only to the original local workspace, not a fresh clone.
No device changes were made during repository preparation.

Prepared 21 November 2026, Europe/Amsterdam. Read this with [the operations runbook](docs/OPERATIONS.md) and [the evidence snapshot](docs/handoff-snapshot.json). The snapshot is generated from local files; it is not a fresh device audit.

## Purpose and user preferences

Maintain the user's original cartridge ROM collections, build a curated family collection, and manage RetroArch on Apple TV. The controller is an original Sony DualShock 4 (PS4), using Apple's MFi input driver.

The user wants 40 games per console, all available reviewed Mickey games prioritized, Disney and Marvel/DC games, real-time adventures, beat-em-ups, family/multiplayer games, and arcade racers, especially Top Gear and Mario Kart. Super Mario RPG is an explicitly allowed exception to avoiding turn-based RPGs. Other RPGs remain cataloged. Suitability notes distinguish co-op, competitive and alternating play; they are editorial guidance, not verified age ratings. For a two-year-old, play is parent-led.

Prefer USA, then World, then Europe ROMs, with explicitly reviewed Japanese exceptions. Do not invent missing games, rankings or authenticity claims. The user authorized configuration changes/uploads, Top 40 synchronization and console bezel installation in the earlier work. This handoff task only creates local context files.

## Current state

| Area | State at handoff | Evidence / remaining check |
| --- | --- | --- |
| Local export | `ROMs/AppleTV-Top40`, six consoles × 40 = 240 games | Current manifests; local hash results in snapshot |
| ROM upload | Earlier sync added 60 ROMs and skipped 180; second run uploaded zero | Prior chat result; equal name/size idempotency, not remote hash checking |
| Device playlists | Replaced old 30-entry lists with six 40-entry lists | Prior device verification; local `.lpl` files also have 40 each |
| Box art | Matching box art for all 240 device playlist entries was verified in earlier work | Prior chat result; visual display for every entry has not been confirmed |
| Audio | 48 kHz CoreAudio/sinc quality 4, 192 ms requested buffer, −3 dB, sync/rate control on | Installed/read-back verified; user reports much better SNES audio after setting Apple TV to 60 Hz |
| Rewind/logging | Rewind, verbose logging and file logging disabled | Current config; supersedes earlier hotkey/load-error records |
| CRT | Global one-pass CRT EasyMode, 4:3, non-integer scaling, Vulkan/vsync/60 Hz | Installed and read-back verified; TV appearance and performance need checking |
| Bezels | Six console PNGs/presets and six content-directory overrides installed | Installation record reports byte-for-byte verification and unchanged main config; appearance needs checking |
| PCE Street Fighter II | Both ports use six-button pads through two game-specific `.opt` profiles | Uploaded/read-back verified; punches/kicks and physical mapping need gameplay check |
| Catalog | CSV snapshots present, 3,837 variants / 3,038 normalized titles | `catalog.sqlite` absent at handoff; no full rescan performed for handoff |
| Version control | Workspace is not a Git repository | Do not assume branches, commits, remotes or a PR exist |

Device RetroArch version recorded during CRT work: **1.22.2**. Last file server: `http://192.168.0.249`. Handoff creation does not contact or modify it. Keep RetroArch open with the web server enabled for future remote work; restarting the app interrupts transfers.

## Workspace map

| Path | Role |
| --- | --- |
| `nes/`, `snes/`, `n64/`, `genesis/`, `master system/`, `PC Engine/` | Original input collections, including nested archives |
| `ROMs/AppleTV-Top40/` | Active generated export; each console has ROMs, `TOP40.md`, `selection.json`; root has `summary.json` |
| `ROMs/AppleTV-Top30/` | Historical 180-game export; do not mistake it for the current set |
| `retroarch.cfg` | Latest local main configuration; hash matches the CRT/bezel installation baseline at handoff |
| `internal/library` (`go run . catalog`) | Recursive scan, archive handling, SQLite/CSV catalog, curated export; defaults to 40 |
| `rom_library/curation.json` | Ordered editorial selection, aliases, language exceptions, family and multiplayer notes, evidence |
| `rom_library/consoles.json` | Console names, folder aliases and cartridge extensions |
| `internal/device` (`sync`, `profile`) | Additive upload of manifest-listed ROMs, defaulting to Top 40 |
| `go run . playlists` | Generates device-path playlists; does not upload them |
| `go run . bezels install` | Preview/install artwork and folder overrides; stops on differing existing files |
| `go run . download master-system` | Optional source downloader; previous missing-game attempts returned 401/403 |
| `*_test.go` (`go test ./...`) | Archive safety/export and sync idempotency/conflict tests |
| `rom_library/state/` | CSV snapshots, extraction cache, backups, playlists, investigation and installation records |
| `rom_library/bezels/` | Six original 2560×1440 PNGs, overlay configs, overrides, source hashes/license and README |

The tooling was rewritten from Python to a Go CLI (cobra + viper, module `github.com/fabiogoma/retroarch`); all Python was removed. Archives are read in pure Go (no libarchive). Locking uses `flock`, so a Unix-like host is assumed. Artwork preparation used Pillow previously; installing the bundled images needs no extra tools.

## Important corrections to older documentation

1. `rom_library/state/verification.json` is the historical **180-game Top 30** report. It is not evidence for the current Top 40; use `docs/handoff-snapshot.json` for the handoff's local checks.
2. `state/apple-tv-core-defaults.json` still says `games: 30`; use it for core identifiers only. Current playlist files have 40 entries each.
3. `state/ps4-hotkeys.json` says rewind is enabled. The later audio change set `rewind_enable = "false"`; the hotkey remains assigned but does nothing until rewind is re-enabled.
4. The earlier integer-scaling/core-aspect setup was superseded by the CRT change: `video_scale_integer = "false"`, `aspect_ratio_index = "0"` (4:3), `video_shader_enable = "true"`.
5. Earlier advice to scan folders and use automatic thumbnail downloading did not fully solve menus/artwork. Direct playlists and label-matched box art were subsequently installed. Existing working lists do not require another scan.
6. Source paths in CSVs/manifests may point to the old root `/Users/fabiom/Documents/ROMs`. Current workspace is the iCloud path shown in `RESUME_WITH_COPILOT.md`. Explicitly rebase paths when using snapshots; do not assume originals were deleted.
7. Many original ROMs are iCloud placeholders. A full scan can hydrate a large library. The Top 40 was previously built using CSV catalog data with rebased paths and selected-file verification. That workaround was a temporary script, not a maintained CLI mode.
8. Local `input_overlay_enable = "false"` is intentional: console content-directory overrides enable the appropriate bezel when a game loads.

## Device layout and core identities

The web server exposes `/config/`, `/playlists/`, `/downloads/`, `/thumbnails/`, `/overlays/` and `/shaders/`. RetroArch itself uses `~/Library/Caches/RetroArch/` for these paths.

| Console folder | Playlist stem / thumbnail system folder | Core framework | Override directory |
| --- | --- | --- | --- |
| `nes` | `Nintendo - Nintendo Entertainment System` | `nestopia.libretro.framework` | `Nestopia` |
| `snes` | `Nintendo - Super Nintendo Entertainment System` | `snes9x.libretro.framework` | `Snes9x` |
| `n64` | `Nintendo - Nintendo 64` | `mupen64plus.next.libretro.framework` | `Mupen64Plus-Next` |
| `genesis` | `Sega - Mega Drive - Genesis` | `genesis.plus.gx.libretro.framework` | `Genesis Plus GX` |
| `master system` | `Sega - Master System - Mark III` | `genesis.plus.gx.libretro.framework` | `Genesis Plus GX` |
| `PC Engine` | `NEC - PC Engine - TurboGrafx 16` | `mednafen.pce.fast.libretro.framework` | `Beetle PCE Fast` |

Playlist core paths begin `:/Frameworks/` and end at the framework bundle. ROM paths begin `~/Library/Caches/RetroArch/downloads/ROMS/<console>/`. Single-ROM ZIP playlist paths include `#<archive_member>`.

Box art lives at `/thumbnails/<system>/Named_Boxarts/<label>.png`, with RetroArch's filename sanitization for reserved characters. Exact label matching matters: the original SNES `Disney's Aladdin` label needed an explicitly matching filename. The working Top 40 playlists now use curated title labels. Bulk art and playlist replacement were performed with temporary `/private/tmp/ra-top40-art.py` and `/private/tmp/ra-install-40.py` scripts; these are not durable project commands and must not be assumed to exist in another agent. No maintained bulk-art uploader exists in `rom_library/` at handoff.

## Audio, picture and controls to preserve

- Apple TV video format was changed to **60 Hz** after logs measured approximately 50 Hz while SNES requested 60.1 fps. The user said, “Done, the audio is much better now.” Only SNES audio was reported tested; no post-change timing measurement was taken.
- `config_save_on_exit = "false"`. Fully close/reopen RetroArch after externally replacing config. Saving an old in-memory config first can undo uploaded settings.
- Global preset `/config/global.slangp` references `../shaders/shaders_slang/crt/shaders/crt-easymode.slang`. Parameters: mask 0.2, scanline strength 0.75, bright boost 1.15. The shader's default cutoff suppresses scanlines for inputs at least 400 pixels tall. N64 performance still needs a device check.
- Bezel artwork is Nosh01's 1440 Plain set from Libretro `overlay-borders`. Every image has a transparent centered 1920×1440 opening for 4:3 gameplay on a 16:9 display. License, URLs and hashes are retained. Genesis and Master System have separate folder overrides despite sharing a core. Moving ROMs or changing cores requires corresponding overrides.
- Local player 1/2 analog-to-D-pad settings are `1`; the intended behavior is left-stick control for digital consoles while retaining native analog control where the core requests it. N64/controller behavior still warrants a gameplay check.
- PCE six-button profiles are `/config/Beetle PCE Fast/Street Fighter II' - Champion Edition.opt` and `/config/Beetle PCE Fast/Street Fighter II' - Champion Edition (Japan).opt`, covering ZIP and internal-ROM basenames. Other core options and global two-button defaults were preserved. Default mapping: Cross=II, Circle=I, Square=III, Triangle=IV, L1=V, R1=VI. L2 switches pad mode and can return to two-button mode; reopening the game restores its saved default.

Previously installed hotkeys (physical mappings have not all been gameplay-verified):

| Combination | Action |
| --- | --- |
| Hold Share | Enable hotkeys |
| Share + Triangle | Quick Menu |
| Share + Square | Toggle FPS |
| Share + Cross | Reset game |
| Share + Options | Close content |
| Share + L1 / R1 | Load / save state |
| Share + L2 / R2 (hold) | Rewind (currently disabled) / fast-forward |
| Share + D-pad Up | Toggle run-ahead (default remains off) |
| L3 + R3 | Quit RetroArch |

## Remaining checks and possible follow-up work

The earlier requested installations are recorded as completed. The next useful step is for the user to restart RetroArch and check one game per console: correct bezel, unobstructed gameplay, CRT appearance, full-speed audio/video, controller input, 40-entry menu and visible box art. In Street Fighter II, check both punches and kicks using the face buttons and L1/R1. Recheck N64 analog control and performance.

If new work is requested, verify current remote state first. A maintained artwork uploader or CSV-path migration could be useful future improvements, but they have not been requested or implemented as part of this handoff.

Three reviewed Mickey titles were unavailable: NES `Mickey Mouse III - Yume Fuusen`, SNES `Mickey to Donald - Magical Adventure 3`, N64 `Dance Dance Revolution - Disney Dancing Museum`. There was no matching Mickey title in the local PC Engine catalog. The current export summary lists these and other missing curated games. Earlier result reported 22 Mickey-related selected entries across consoles.

Master System downloads from the supplied Archive.org source failed with authorization errors; no additional ROMs were downloaded. Respect those failures and preserve existing collections.

## Evidence and backups

- `rom_library/README.md`: catalog/export architecture and selection policy.
- `rom_library/state/audio-quality/README.md` and `preparation.json`: audio changes, verified upload/backups, 60 Hz user result.
- `rom_library/state/crt-quality/installation.json`, `global.slangp`: exact CRT changes, hashes and backup paths.
- `rom_library/bezels/README.md`, `sources.json`, `state/bezels/installation.json`: bezel sources, installed file hashes, removal instructions.
- `rom_library/state/pce-controller/README.md`, `installation.json`: six-button profile scope, hashes and removal instructions.
- `rom_library/state/ps4-hotkeys*.json`: historical hotkey installation, subject to later rewind change.
- `rom_library/state/load-error-investigation/repair.json`: earlier playlist/core-path repair; debug logging later disabled.
- `rom_library/state/before-top40-playlists-20261121-174009/`: backed-up pre-Top40 playlist files.

Context was cross-checked against the available Codex chats titled “Inspect project and summarize”, “Improve audio quality”, “Optimize CRT shaders for all games”, “Fix PC Engine controller mapping”, and “Add artwork to side bars”. Local installation records were preferred when a chat was still finishing. No unrelated chat content is included.
