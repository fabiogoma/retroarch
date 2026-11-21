# ROM library operations

For a fresh checkout, start with the root `README.md` and `docs/DOWNLOADS.md`.
Download content separately; exports, state, device profiles, the root config and
the dated evidence snapshot below are local-only and are not included in Git.
The following device records describe the original installation, not the state
of a newly configured Apple TV.

Run commands from the project root (ROM files live in `ROMs/`). Read `COPILOT_HANDOFF.md` before making changes. All commands below are existing project commands, except the explicit read-only `curl` examples. No remote operation was run while creating this handoff.

## Inspect and test

Start with `ROMs/AppleTV-Top50/summary.json`, its six `selection.json` files, and `rom_library/state/apple-tv-playlists/*.lpl`. Use `docs/handoff-snapshot.json` for dated file hashes and local export validation. CSV source paths may contain the old workspace root; the SQLite catalog was absent at handoff.

```sh
go version
go test ./...
```

Go 1.24+ is required. Tests cover single-ROM ZIP retention, nested extraction, traversal rejection, region selection/stale catalog entries, failed-export preservation, the real outer-RAR detection regression (set `RETROARCH_TEST_RAR` to a RAR path to enable it; skipped otherwise), repeat sync and remote conflicts. Tests use temporary outputs, not a complete real-library rebuild.


## Rebuild a selection when requested

Edit `rom_library/curation.json` to change picks. Do not edit a generated selection manually and expect it to survive rebuilding.

```sh
go run . catalog
```

This performs a full recursive source scan, refreshes catalog outputs and rebuilds `ROMs/AppleTV-Top50`. It can hydrate many iCloud files and take substantial time. Original ROMs remain in place. The last generated export is retained at `rom_library/state/previous-export`; a subsequent successful rebuild replaces that backup. Keep personal files outside managed exports/cache directories.

Optional modes:

```sh
go run . catalog --from-catalog
go run . catalog --unzip-export
go run . catalog --count 30
```

`--from-catalog` reuses the existing `catalog.sqlite` (or imports `catalog.csv`, remapping moved paths) and skips re-scanning multi-gigabyte source archives. The third example intentionally generates a separate Top 30 export. Normal operation defaults to 50. A single recognized ROM in a ZIP stays compressed; collection ZIPs and RAR/7z bundles are safely extracted to managed cache. Preserve path/link rejection, nesting/size limits and forced outer-format selection. SHA-256 checks consistency, not preservation-DAT authenticity. Disc sets, arcade ZIPs and BIOS workflows need separate profile support.

After rebuilding, inspect counts, missing picks and issues in `summary.json`; verify exported file sizes and `export_sha256` hashes against each manifest. New consoles require reviewed curation and suitable profiles.

## Check the Apple TV connection

Open RetroArch on Apple TV and enable its web file server. Last known address:

```sh
curl --fail --silent --show-error --connect-timeout 5 --max-time 10 --get --data-urlencode 'path=/' 'http://192.168.0.249/list'
```

Do not infer that a refusal means the ROMs/config are broken. The app may be closed, the server disabled or the address changed. Use `--server` on upload commands when the device address changes.

For read-only download into a temporary file:

```sh
curl --fail --silent --show-error --connect-timeout 5 --max-time 15 --get --data-urlencode 'path=/config/retroarch.cfg' 'http://192.168.0.249/download' --output /private/tmp/retroarch-device-inspect.cfg
```

## Sync ROMs

```sh
go run . sync --dry-run
go run . sync
```

Optional: `--server http://NEW_ADDRESS` or `--export AppleTV-TopN`.

The command uploads manifest-listed ROMs to `/downloads/ROMS/<console>/`. Same-name/same-size files are skipped; same-name/different-size files stop the operation. Pending local files are checked against their manifest SHA-256 before upload; new uploads are checked remotely by name/size. Existing remote files are not downloaded for hashing. Nothing is deleted. A rerun resumes missing uploads and should upload zero after completion.

This command does **not** install playlists, thumbnails, shaders, controller options or bezels.

## Regenerate playlists

```sh
go run . playlists
```

This writes local `.lpl` files and `generation.json` in `rom_library/state/apple-tv-playlists/`. Default export is Top 50. Optional arguments: `--export`, `--output`, `--device-home` (default `~`). It does not upload.

For device installation, first download/back up all active `/playlists/<system>.lpl` files. Upload staged replacements and activate them only after checking the live server's supported behavior. Verify active files by downloading them again, parsing JSON and checking 50 entries, ROM/member paths and core framework bundle paths. Preserve original playlists remotely and locally. Merely uploading additional ROMs does not change menu counts.

The existing server has been observed numbering duplicate uploaded names rather than replacing the active file. Its supported `/move` endpoint was used previously with form fields `oldPath` and `newPath`. Inspect the current server's JavaScript or stored `rom_library/state/crt-quality/file-server-index.js` before relying on this behavior; there is no maintained playlist installer CLI.

## Box art

Current device art was previously verified for all 240 playlist entries. Recheck only when troubleshooting or after changing labels. Files belong in `/thumbnails/<system>/Named_Boxarts/` with the sanitized playlist label as basename. Libretro thumbnail database names may differ from curated labels; fetch the matching game's image and store it under the label RetroArch uses. Preserve correct existing art and explicit alias choices.

Prior matching needed special handling for Wave Race 64/Kawasaki Jet Ski, Ys, The NewZealand Story, The Disney Collection, Mickey's Adventure in Numberland, and Mickey's Playtown Adventure. Do not silently choose unrelated artwork from an approximate match. No durable bulk artwork installer or complete persistent artwork mapping was created; previous temporary scripts are not required to resume context.

## CRT shader and bezel maintenance

Current global shader is `/config/global.slangp`; the local copy is `rom_library/state/crt-quality/global.slangp`. Back up/fetch current config and preset before altering them. Preserve unrelated audio/controller settings, verify activated bytes, then fully restart RetroArch without saving an old in-memory config.

The bundled bezel installer can preview device differences and install missing matching assets:

```sh
go run . download bezels
go run . bezels install
go run . bezels install --apply
```

Preview performs device reads and rewrites the local generated bezel configs/overrides, but does not upload. It requires automatic overrides and 4:3 to be enabled, checks playlist ROM directories, and refuses unexpected overrides or differing destinations. `--install` backs up the main config and uploads/byte-verifies PNGs, presets, provenance/license and six directory overrides. It verifies that the main config stayed unchanged. Reload a game to activate.

To remove bezels, follow `rom_library/bezels/README.md`: remove only the six added folder override files, then reload. Do not roll back the entire main config to remove them.

To remove Street Fighter's six-button override, remove only the two game `.opt` files named in `rom_library/state/pce-controller/installation.json`. The downloaded core defaults and main config there are pre-change evidence, not replacement configs to upload wholesale.

## Report results accurately

Separate local checks, remote read-back verification and user-observed gameplay. A byte-identical config does not prove that the picture looks good or the controller works. Update the handoff after material changes, preserving the date and evidence. New remote reads are necessary to establish current device state; the saved installation reports are historical.

## Push the main config

```sh
go run . config push --dry-run
go run . config push            # uploads ./retroarch.cfg
```

Replaces `/config/retroarch.cfg` with `--file` (default `<root>/retroarch.cfg`). The current device file is saved to `rom_library/state/config-backups/` and kept remotely as `retroarch.cfg.push-backup-<id>`. The upload is staged under a unique name, hash-verified, then swapped in and read back. It stops if the remote file changes mid-push. Fully restart RetroArch afterwards without saving its in-memory config.
