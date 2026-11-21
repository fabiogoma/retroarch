# Download ROMs and artwork outside Git

`go run . download` uses the [Internet Archive metadata API](https://archive.org/developers/md-read.html)
to locate explicitly named files and their published sizes/checksums. It downloads
from `https://archive.org/download/<item>/<file>`, preserving nested source paths in
the URL. Local destinations are explicit, so curated labels can differ from source
filenames. There is no fuzzy artwork matching or automatic collection scraping.

List an item's files before configuring a download:

```sh
go run . download list master_system
```

`master_system` is the only original Archive item supplied to this project. Prior
downloads returned HTTP 401/403. Listing metadata does not prove its files are
accessible. Choose accessible sources you are permitted to use; no complete
six-console or artwork source set is bundled or claimed verified.

Copy `rom_library/archive_sources.example.json` to `archive_sources.local.json`.
Replace its empty `files` list with mappings using exact identifiers and filenames
from your chosen items. This schema example uses placeholders, not working sources:

```json
{
  "version": 1,
  "files": [
    {
      "item": "YOUR-ROM-ITEM",
      "file": "NES/Exact Game Name (USA).zip",
      "destination": "nes/archive.org/Exact Game Name (USA).zip"
    },
    {
      "item": "YOUR-ARTWORK-ITEM",
      "file": "boxart/Exact Game Name.png",
      "destination": "artwork/Nintendo - Nintendo Entertainment System/Named_Boxarts/Playlist Label.png"
    }
  ]
}
```

An optional `sha256` field on each entry pins an independently recorded checksum.
Otherwise the downloader uses Archive's SHA-1 and/or MD5 metadata, requiring at
least one checksum. Integrity checks establish consistency with the source, not
authenticity against a preservation database. A file may be at most 32 GiB.

```sh
go run . download manifest archive_sources.local.json --dry-run
go run . download manifest archive_sources.local.json
```

Preview reads metadata and checks existing local destinations but writes nothing.
Downloads stream to temporary files, verify exact size and advertised checksums,
then activate without overwriting. Reruns verify/skip existing files; differing
files stop the operation. Interrupted or rejected downloads leave no partial ROM.
The next run retries the whole unfinished file and skips completed files. HTTP
401/403 stops immediately; there is no authentication bypass.

HTTPS certificate verification stays enabled and uses the operating system trust
store (`SSL_CERT_FILE` / `SSL_CERT_DIR` overrides are respected). Certificate errors
require fixing the OS trust configuration, not disabling verification.

Destinations must be inside a configured console folder (`nes/`, `snes/`, `n64/`,
`genesis/`, `master system/`, `PC Engine/`, `gb/`, `gbc/`, `gba/`), `artwork/`, or
`rom_library/bezels/<name>.png`. Absolute/traversal paths, symlinks and duplicate
destinations are refused. Console and artwork destinations are resolved under `--roms`; bezels under `--root`.

After downloading ROMs, run `go run . catalog` to build an export, then use the existing
sync/playlist commands. Box art remains local until installed under the matching
device thumbnail path. No artwork installer for arbitrary downloaded images is
included; use the web file server or capture/restore a complete existing setup.

The six known bezel images have original upstream URLs and SHA-256 hashes in
`rom_library/bezels/sources.json`; `--bezels` downloads them from Libretro's GitHub
repository. The attribution/license stays in Git, while the PNGs remain local.
If the identical images are available in an Archive item, map those exact files
to `rom_library/bezels/nes.png`, etc. through a manifest instead. The installer
verifies against the pinned hashes regardless of the download origin.

The older `go run . download master-system` remains a compatibility command for its
single source. Prefer the manifest-based downloader for new work.
