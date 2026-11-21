# RetroArch Apple TV Tool

A simple Go CLI for building curated **Top 10** through **Top 100** (in steps of 10) ROM sets from your local `ROMs/` folder, downloading box art and console bezels, and syncing everything to RetroArch on Apple TV in parallel.

Consoles supported: **NES**, **SNES**, **Nintendo 64**, **Genesis**, **Master System**, and **PC Engine**.

## Quick start

Run commands from the project root (Go 1.24+ required; ROM files and single-ROM `.zip`s are expected directly inside each `ROMs/<console>/` folder):

```sh
# 1. Scan ROMs/ and build AppleTV-Top10 through AppleTV-Top100 (with playlists)
go run . build

# 2. Download box art and console bezel PNGs locally
go run . artwork --top 50

# 3. Sync ROMs, playlists, artwork, bezels, and DualShock 4 config to Apple TV
go run . sync --top 50 --server http://192.168.0.249 --dry-run
go run . sync --top 50 --server http://192.168.0.249
```

Restart RetroArch on Apple TV (without saving its stale in-memory config) after syncing config changes.

## Commands

| Command | What it does |
|---|---|
| `retroarch build` | Scans `ROMs/` (or `--from-cache`), selects games using `config/curation.yaml` (personal preferences + best-selling games), and builds `ROMs/AppleTV-Top10` through `AppleTV-Top100` (steps of 10) with ready-to-sync `.lpl` playlists. Use `--top 10,20` or `--top 60,70,80,90,100` for specific sets or `--unzip` to extract single-ROM ZIPs. |
| `retroarch artwork` | Downloads missing game box art from Libretro's official thumbnail repositories into `artwork/<System>/Named_Boxarts/` and hash-pinned console bezel PNGs into `config/bezels/`. Runs in parallel (`--workers`). |
| `retroarch sync` | Syncs **ROMs**, **playlists**, **artwork**, **bezels**, and **configuration** to RetroArch's web file server in parallel (`--workers`, default `4`). Supports `--top 10\|20\|30\|40\|50\|60\|70\|80\|90\|100`, `--only roms,playlists,artwork,bezels,config`, `--dry-run`, and `--verbose` (`-v`) for per-file details. Never deletes remote ROMs and backs up replaced configs/playlists. |
| `retroarch check` | Verifies no ROMs, artwork, or local state are tracked in Git. |

## DualShock 4 (2 Controllers) Configuration

`config/apple-tv.cfg` is merged into `/config/retroarch.cfg` during `sync` (preserving unrelated device settings):

- **2 original Sony DualShock 4 controllers** over Apple TV MFi (`input_joypad_driver = "mfi"`, `input_max_users = "2"`, ports 1 & 2 mapped to joypad indices `0` and `1`).
- **Left analog stick as D-pad**: enabled (`input_player1_analog_dpad_mode = "1"`, `input_player2_analog_dpad_mode = "1"`) for all D-pad-only consoles (NES, SNES, Genesis, Master System, PC Engine), and disabled (`"0"`) via per-folder override on **Nintendo 64** so N64 uses the native analog stick cleanly.
- **PC Engine 6-button**: `config/pce-six-button.opt` is synced automatically for Street Fighter II (both players) while other PC Engine games keep 2-button defaults.
- **Video & Audio**: 60 Hz Vulkan output, 4:3 aspect ratio, CRT EasyMode (`config/global.slangp`), console bezels via per-folder overrides, and stable CoreAudio settings.
- **iCloud Cloud Sync**: Automatic CloudKit sync (`cloud_sync_driver = "icloud"`) enabled for save files (`.srm`) and save states (non-destructive; configs/thumbnails/system disabled so they don't conflict with `sync`).

## Configuration

All parameters live in `config/retroarch-tool.yaml` (server URL, local and remote directories, default Top sets, concurrency workers, timeouts, artwork URLs).

For personal/machine-specific settings such as your Apple TV IP address, create `config/retroarch-tool.local.yaml` (gitignored — automatically merged on top of `config/retroarch-tool.yaml`), or override with `RETROARCH_*` environment variables or CLI flags (`--server`, `--roms`, `--workers`, `--top`).

Before updating any remote config file, `sync` downloads and backs up the current running RetroArch configuration to `state/config-backups/<timestamp>/` (configurable via `backup_dir`). To restore a backup if anything goes wrong:

```sh
go run . sync --restore-config state/config-backups/<timestamp>
```

All custom configuration lives in `config/` (YAML for local project config; native `.cfg`/`.slangp`/`.opt` formats where required by RetroArch):
- `config/retroarch-tool.yaml` (+ optional gitignored `config/retroarch-tool.local.yaml`) — CLI settings, server URL, paths, and concurrency.
- `config/curation.yaml` — your game preferences and Top 10–100 ranking (re-run `go run . build --from-cache` after editing).
- `config/consoles.yaml` & `config/apple_tv_cores.yaml` — console extensions and Apple TV core mappings.
- `config/apple-tv.cfg`, `config/global.slangp`, `config/pce-six-button.opt` — RetroArch overrides, CRT shader preset, and PCE 6-button options.
- `config/bezels/` — console bezel metadata (`sources.yaml`) and per-console DualShock 4 + bezel overrides (`.cfg`).

## Testing

```sh
go test ./...
go run . check
```

