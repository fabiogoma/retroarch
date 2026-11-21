# RetroArch Apple TV project instructions

Read `README.md`.

## Project

- Go CLI (cobra + viper) with 4 subcommands: `build`, `artwork`, `sync`, `check`.
- Builds curated Top 30, Top 40, and Top 50 export folders (`ROMs/AppleTV-Top30`, `AppleTV-Top40`, `AppleTV-Top50`) with Apple TV `.lpl` playlists from local ROMs using `config/curation.yaml`.
- Downloads box art from Libretro's official `libretro-thumbnails` repos and hash-pinned bezel PNGs locally (`artwork/` and `config/bezels/`) in parallel.
- Syncs ROMs, playlists, artwork, bezels, and RetroArch configuration in parallel with goroutines to the Apple TV web file server (configured in `retroarch-tool.yaml` / `retroarch-tool.local.yaml`).
- Never commit ROMs, artwork, exports, or local state. Run `go run . check` and `go test ./...`.

## Preserve these decisions

- Never move or delete original ROMs in `ROMs/`. All custom configuration lives in `config/` (edit game selection in `config/curation.yaml`). Use YAML for all local project configuration (`retroarch-tool.yaml`, `config/*.yaml`, `config/bezels/sources.yaml`); keep RetroArch's native formats (`.cfg`, `.slangp`, `.opt`, `.lpl`) as required by RetroArch.
- Sync is additive for ROMs and artwork (never deletes remote files); playlists and configs are staged under unique names, backed up, and swapped via `/move`.
- Device playlist paths use `~/Library/Caches/RetroArch/...`; cores use `:/Frameworks/<core>.framework` bundle paths.
- Controllers: 2 original Sony DualShock 4 controllers via Apple TV MFi (`input_max_users = "2"`, ports 1 & 2 mapped to joypad indices `0` and `1`). D-pad-only consoles enable `analog_dpad_mode = "1"` for both players; N64 overrides it to `"0"`. Street Fighter II (PC Engine) uses 6-button options for both players.
- Save-on-exit is disabled (`config_save_on_exit = "false"`). Restart RetroArch after config sync without saving stale in-memory settings.

