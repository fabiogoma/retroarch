# Console bezels for Apple TV

PNG artwork is excluded from Git. Before installing from a fresh checkout, fetch
the unchanged, SHA-256-pinned upstream images:

```sh
go run . download bezels
```

Internet Archive mappings can supply identical images instead; see
`docs/DOWNLOADS.md`. The installation account below refers to the original Apple TV.

Nosh01's 1440 Plain artwork from Libretro's overlay-borders collection is installed for NES, SNES, Genesis, Master System, PC Engine and Nintendo 64. Original PNGs are unchanged; sources and hashes are in sources.yaml, and the repository license is included in LICENSE.

Each 2560×1440 image has a fully transparent, centered 1920×1440 gameplay region. The overlay stretches over the 16:9 display, while the existing 4:3 game picture fills its transparent opening. The presets contain no touch controls. Opacity is 100%, automatic overlay scaling/rotation is disabled, connected controllers do not hide the artwork, and opening menus hides the artwork.

Artwork is stored on Apple TV under /overlays/console-bezels/. Six content-directory overrides in /config/<core>/<console-folder>.cfg select the artwork automatically for all 240 current playlist entries and future games added to the same console folders. Genesis and Master System have separate folder overrides despite using the same core. Changing a console's core or moving its ROMs to another folder requires a corresponding override.

Reload a game to activate its bezel. Restarting RetroArch first is also fine. The global config, CRT shader, audio, controller mappings and core options are unchanged. Installation verifies remote files byte for byte; actual TV playback must be checked on the device.

From the ROMs root, preview or reinstall missing identical files:

```sh
go run . bezels install
go run . bezels install --apply
```

The installer stops if any destination has different contents. It also stops for unexpected core/game overrides rather than overwriting them. Installation metadata and a copy of the original main configuration are under rom_library/state/bezels/.

To remove this installation, delete only these six added override files using the RetroArch web server, then reload the game:

- /config/Nestopia/nes.cfg
- /config/Snes9x/snes.cfg
- /config/Genesis Plus GX/genesis.cfg
- /config/Genesis Plus GX/master system.cfg
- /config/Beetle PCE Fast/PC Engine.cfg
- /config/Mupen64Plus-Next/n64.cfg

The now-unused /overlays/console-bezels/ folder can then be removed. Do not replace the main configuration to remove bezels; it was never changed by this installation.

References:

- https://github.com/libretro/overlay-borders
- https://docs.libretro.com/development/retroarch/input/overlay/
- https://docs.libretro.com/guides/overrides/
