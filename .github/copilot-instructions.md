# ROM library project instructions

Read `README.md`, then `docs/OPERATIONS.md`. `COPILOT_HANDOFF.md` preserves historical local context. Its evidence snapshot, state files, exports and device profile are ignored and may be absent in a fresh checkout. No access to prior chats/tools is required.

## Project

- This is the source repository for RetroArch Apple TV setup tooling: https://github.com/fabiogoma/retroarch. ROMs, artwork, BIOS, device snapshots, exports, local configs and state must never be committed. Use the source allowlist and run `rom_library/check_repository.py` before publishing.
- Use `download_assets.py` and an explicit local Archive manifest for new downloads. Complete sources for all consoles/artwork have not been supplied; do not invent them or bypass access denial. Known bezels may be fetched from their pinned upstream sources.
- Active export: `ROMs/AppleTV-Top40`, 40 games for each of NES, SNES, N64, Genesis, Master System and PC Engine (240 total). `ROMs/AppleTV-Top30` is historical.
- Python 3.11+, standard library only; archive support uses system libarchive. `uv` is the documented runner. Commands run from the project root (ROM files live in `ROMs/`).
- Device file server last used: `http://192.168.0.249`. It requires RetroArch to be open with its web server enabled. Availability and the IP can change.

## Preserve these decisions

- Never move/delete original ROMs. Edit selection policy in `rom_library/curation.json`, not generated manifests. Preserve family/multiplayer notes, Mickey priority, arcade racing preferences and the Super Mario RPG exception.
- Do not start a complete rescan just to learn the project. Many source files are iCloud placeholders. Read manifests/CSV snapshots first; `rom_library/state/catalog.sqlite` was absent at handoff.
- Generated exports and `state/previous-export` are replaceable outputs. Keep personal files out of them. Preserve extraction safety checks and explicit outer archive format selection.
- Sync is additive: skip equal name/size, stop on differing size, never delete remote ROMs. This is not cryptographic validation of existing remote files. Sync does not update playlists, artwork or settings.
- Device playlist paths use `~/Library/Caches/RetroArch/...`; cores use `:/Frameworks/<core>.framework` bundle paths. Never substitute desktop paths or append framework executable names.
- Before replacing device config/playlists, fetch and back up the current remote files, merge only intended changes, then download and verify the result. Duplicate uploads may receive numbered names; do not assume an upload replaced the active file.
- Keep current 60 Hz output, audio stability settings, global CRT preset and console bezels unless the requested change calls for adjustment. The main config disables overlays globally; directory overrides enable console bezels. That is intentional.
- Save-on-exit is disabled. After external config changes, fully restart RetroArch without saving its stale in-memory configuration first.
- Controller is an original Sony DualShock 4 with the MFi driver. Street Fighter II uses game-specific six-button PCE options; other PCE games retain two-button defaults. Rewind is currently disabled despite the old hotkey record saying enabled.
- Distinguish file verification from gameplay validation. SNES audio improvement at 60 Hz was user-confirmed. CRT appearance, bezel appearance and PCE six-button gameplay still need on-device checks.

## Working commands

```sh
python3 -B -m unittest discover -s rom_library -p 'test_*.py' -v
python3 -B rom_library/check_repository.py
python3 -B rom_library/sync_apple_tv.py --dry-run
```

The full test suite includes a real PC Engine RAR fixture that can trigger iCloud download. See the runbook before exporting, uploading or installing. Make requested changes directly within scope, preserve unrelated settings, and update the handoff when material state changes.
