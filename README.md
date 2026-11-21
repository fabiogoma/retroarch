# RetroArch Apple TV Tool

A simple Go CLI for building curated **Top 30**, **Top 40**, and **Top 50** ROM sets from your local `ROMs/` folder, downloading box art and console bezels, and syncing everything to RetroArch on Apple TV in parallel.

Consoles supported: **NES**, **SNES**, **Nintendo 64**, **Genesis**, **Master System**, and **PC Engine**.

## Quick start

Run commands from the project root (Go 1.24+ required; archive support for ZIP/7z/RAR/tar/tgz is pure Go):

```sh
# 1. Scan ROMs/ and build AppleTV-Top30, AppleTV-Top40, and AppleTV-Top50 (with playlists)
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
| `retroarch build` | Scans `ROMs/` (or `--from-cache`), selects games using `rom_library/curation.json` (personal preferences + best-selling games), and builds `ROMs/AppleTV-Top30`, `AppleTV-Top40`, and `AppleTV-Top50` with ready-to-sync `.lpl` playlists. Use `--top 50` for a single set or `--unzip` to extract single-ROM ZIPs. |
| `retroarch artwork` | Downloads missing game box art from Libretro's official thumbnail repositories into `artwork/<System>/Named_Boxarts/` and hash-pinned console bezel PNGs into `rom_library/bezels/`. Runs in parallel (`--workers`). |
| `retroarch sync` | Syncs **ROMs**, **playlists**, **artwork**, **bezels**, and **configuration** to RetroArch's web file server in parallel (`--workers`, default `4`). Supports `--top 30|40|50`, `--only roms,playlists,artwork,bezels,config`, and `--dry-run`. Never deletes remote ROMs and backs up replaced configs/playlists. |
| `retroarch check` | Verifies no ROMs, artwork, or local state are tracked in Git. |

## DualShock 4 (2 Controllers) Configuration

`config/apple-tv.cfg` is merged into `/config/retroarch.cfg` during `sync` (preserving unrelated device settings):

- **2 original Sony DualShock 4 controllers** over Apple TV MFi (`input_joypad_driver = "mfi"`, `input_max_users = "2"`, ports 1 & 2 mapped to joypad indices `0` and `1`).
- **Left analog stick as D-pad**: enabled (`input_player1_analog_dpad_mode = "1"`, `input_player2_analog_dpad_mode = "1"`) for all D-pad-only consoles (NES, SNES, Genesis, Master System, PC Engine), and disabled (`"0"`) via per-folder override on **Nintendo 64** so N64 uses the native analog stick cleanly.
- **PC Engine 6-button**: `config/pce-six-button.opt` is synced automatically for Street Fighter II (both players) while other PC Engine games keep 2-button defaults.
- **Video & Audio**: 60 Hz Vulkan output, 4:3 aspect ratio, CRT EasyMode (`config/global.slangp`), console bezels via per-folder overrides, and stable CoreAudio settings.

## Configuration

All parameters live in `retroarch-tool.yaml` (server URL, local and remote directories, default Top sets, concurrency workers, timeouts, artwork URLs).

For personal/machine-specific settings such as your Apple TV IP address, create `retroarch-tool.local.yaml` (gitignored — automatically merged on top of `retroarch-tool.yaml`), or override with `RETROARCH_*` environment variables or CLI flags (`--server`, `--roms`, `--workers`, `--top`).

Before updating any remote config file, `sync` downloads and backs up the current running RetroArch configuration to `rom_library/state/config-backups/<timestamp>/` (configurable via `backup_dir`). To restore a backup if anything goes wrong:

```sh
go run . sync --restore-config rom_library/state/config-backups/<timestamp>
```

Edit `rom_library/curation.json` to adjust your game preferences and ranking, then re-run `go run . build --from-cache`.

## Testing

```sh
go test ./...
go run . check
```

