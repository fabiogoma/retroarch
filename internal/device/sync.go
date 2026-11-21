package device

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/util"
)

// Remote directories on the Apple TV RetroArch web file server (configurable via config/retroarch-tool.yaml).
var (
	RemoteROMRoot       = []string{"downloads", "ROMS"}
	RemotePlaylistsDir  = "playlists"
	RemoteThumbnailsDir = "thumbnails"
	RemoteOverlaysDir   = []string{"overlays", "console-bezels"}
	RemoteConfigDir     = "config"
)

// SyncOptions controls what gets synced from local to the remote RetroArch server.
type SyncOptions struct {
	ExportDir  string
	ArtworkDir string
	BezelDir   string
	ConfigDir  string
	StateDir   string
	BackupDir  string
	Workers    int
	DryRun     bool
	Verbose    bool
	Out        io.Writer
	ROMs       bool
	Playlists  bool
	Artwork    bool
	Bezels     bool
	Config     bool
}

type uploadJob struct {
	group  string // console folder or category for grouped logging
	folder string
	local  string
	size   int64
	sha256 string // optional pre-upload local SHA-256 check
}

type consoleGroup struct {
	name  string
	jobs  []uploadJob
	bytes int64
}

const maxConfigDiffLines = 8

// SyncAll uploads ROMs, playlists, artwork, bezels, and configuration to the RetroArch
// web file server, using parallel goroutines for bulk transfers.
func SyncAll(server Remote, o SyncOptions) error {
	if o.Workers < 1 {
		o.Workers = 4
	}
	out := o.Out
	if out == nil {
		out = os.Stdout
	}
	backupRoot := o.BackupDir
	if backupRoot == "" {
		backupRoot = filepath.Join(o.StateDir, "config-backups")
	}
	runBackupDir := filepath.Join(backupRoot, time.Now().UTC().Format("20060102T150405Z"))

	exportName := filepath.Base(o.ExportDir)
	if o.DryRun {
		fmt.Fprintf(out, "Sync preview (dry-run) -> %s (%s)\n", server.URL(), exportName)
	} else {
		fmt.Fprintf(out, "Sync -> %s (%s)\n", server.URL(), exportName)
	}

	totalChanges := 0
	configChanged := false

	if o.ROMs {
		n, err := syncROMs(out, server, o.ExportDir, o.Workers, o.DryRun, o.Verbose)
		if err != nil {
			return fmt.Errorf("sync roms: %w", err)
		}
		totalChanges += n
	}
	if o.Playlists {
		n, err := syncPlaylists(out, server, o.ExportDir, o.DryRun)
		if err != nil {
			return fmt.Errorf("sync playlists: %w", err)
		}
		totalChanges += n
	}
	if o.Artwork {
		n, err := syncArtwork(out, server, o.ExportDir, o.ArtworkDir, o.Workers, o.DryRun, o.Verbose)
		if err != nil {
			return fmt.Errorf("sync artwork: %w", err)
		}
		totalChanges += n
	}
	if o.Bezels {
		n, err := syncBezels(out, server, o.BezelDir, runBackupDir, o.Workers, o.DryRun, o.Verbose)
		if err != nil {
			return fmt.Errorf("sync bezels: %w", err)
		}
		totalChanges += n
	}
	if o.Config {
		n, err := syncConfig(out, server, o.ExportDir, o.ConfigDir, runBackupDir, o.DryRun, o.Verbose)
		if err != nil {
			return fmt.Errorf("sync config: %w", err)
		}
		totalChanges += n
		configChanged = n > 0
	}

	if o.DryRun {
		if totalChanges == 0 {
			fmt.Fprintln(out, "No changes needed (dry-run).")
		} else {
			fmt.Fprintln(out, "Dry-run complete (no files modified).")
		}
	} else if configChanged {
		fmt.Fprintln(out, "Fully restart RetroArch (without saving its in-memory config) to load the updated config.")
	}
	return nil
}

func syncROMs(out io.Writer, server Remote, exportDir string, workers int, dryRun, verbose bool) (int, error) {
	selections, err := library.LoadExportSelections(exportDir)
	if err != nil {
		return 0, err
	}
	consoles := make([]string, 0, len(selections))
	for c := range selections {
		consoles = append(consoles, c)
	}
	sort.Strings(consoles)

	var pending []uploadJob
	var groups []consoleGroup
	var totalBytes int64
	skipped := 0
	for _, console := range consoles {
		picks := selections[console]
		if len(picks) == 0 {
			continue
		}
		parts := append(append([]string{}, RemoteROMRoot...), console)
		folder, err := server.EnsureFolder(parts, dryRun)
		if err != nil {
			return 0, err
		}
		remote := map[string]Entry{}
		if folder != "" {
			if remote, err = server.List(folder); err != nil {
				return 0, err
			}
		}
		display := folder
		if display == "" {
			display = "/" + strings.Join(parts, "/") + "/"
		}
		grp := consoleGroup{name: console}
		for _, p := range picks {
			if !util.SafeName(p.File) {
				return 0, fmt.Errorf("invalid ROM name: %q", p.File)
			}
			local := filepath.Join(exportDir, console, p.File)
			info, err := os.Lstat(local)
			if err != nil || !info.Mode().IsRegular() || info.Size() != p.Bytes {
				return 0, fmt.Errorf("local ROM does not match manifest: %s", local)
			}
			existing, ok := remote[p.File]
			switch {
			case !ok:
				job := uploadJob{group: console, folder: display, local: local, size: p.Bytes, sha256: p.ExportSHA256}
				pending = append(pending, job)
				grp.jobs = append(grp.jobs, job)
				grp.bytes += p.Bytes
				totalBytes += p.Bytes
			case existing.Size != nil && *existing.Size == p.Bytes:
				skipped++
			default:
				return 0, fmt.Errorf("remote file exists with a different size or type: %s%s", display, p.File)
			}
		}
		if len(grp.jobs) > 0 {
			groups = append(groups, grp)
		}
	}

	if len(pending) == 0 {
		fmt.Fprintf(out, "ROMs:      up to date (%d already present)\n", skipped)
		return 0, nil
	}

	if !dryRun {
		// Verify local hashes in parallel before uploading.
		if err := runParallel(workers, len(pending), func(_ context.Context, i int) error {
			j := pending[i]
			sum, err := util.FileSHA256(j.local)
			if err != nil {
				return err
			}
			if sum != j.sha256 {
				return fmt.Errorf("local ROM hash mismatch: %s", j.local)
			}
			return nil
		}); err != nil {
			return 0, err
		}

		if err := uploadJobsParallel(server, pending, workers); err != nil {
			return 0, err
		}
	}

	verb := "uploaded"
	if dryRun {
		verb = "would upload"
	}
	fmt.Fprintf(out, "ROMs:      %s %d (%s), %d already present\n", verb, len(pending), formatBytes(totalBytes), skipped)
	writeGroupedUploads(out, groups, verbose)
	return len(pending), nil
}

func syncPlaylists(out io.Writer, server Remote, exportDir string, dryRun bool) (int, error) {
	plDir := filepath.Join(exportDir, "playlists")
	entries, err := os.ReadDir(plDir)
	if err != nil {
		return 0, fmt.Errorf("read playlists in %s (run `build` first): %w", plDir, err)
	}
	folder, err := server.EnsureFolder([]string{RemotePlaylistsDir}, dryRun)
	if err != nil {
		return 0, err
	}
	remote := map[string]Entry{}
	if folder != "" {
		if remote, err = server.List(folder); err != nil {
			return 0, err
		}
	}
	type plChange struct {
		action string
		path   string
	}
	var changes []plChange
	skipped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lpl") {
			continue
		}
		local := filepath.Join(plDir, e.Name())
		data, err := os.ReadFile(local)
		if err != nil {
			return 0, err
		}
		remotePath := "/" + RemotePlaylistsDir + "/" + e.Name()
		action := "add"
		if _, exists := remote[e.Name()]; exists {
			cur, err := Fetch(server, remotePath)
			if err != nil {
				return 0, err
			}
			if bytes.Equal(cur, data) {
				skipped++
				continue
			}
			action = "update"
		}
		if !dryRun {
			if err := replaceRemoteFile(server, remotePath, data, "backup"); err != nil {
				return 0, err
			}
		}
		changes = append(changes, plChange{action: action, path: remotePath})
	}
	if len(changes) == 0 {
		fmt.Fprintf(out, "Playlists: up to date (%d identical)\n", skipped)
		return 0, nil
	}
	verb := "synced"
	if dryRun {
		verb = "would sync"
	}
	fmt.Fprintf(out, "Playlists: %s %d, %d identical\n", verb, len(changes), skipped)
	for _, ch := range changes {
		fmt.Fprintf(out, "  %-6s %s\n", ch.action, ch.path)
	}
	return len(changes), nil
}

func syncArtwork(out io.Writer, server Remote, exportDir, artworkDir string, workers int, dryRun, verbose bool) (int, error) {
	selections, err := library.LoadExportSelections(exportDir)
	if err != nil {
		return 0, err
	}
	var pending []uploadJob
	var groups []consoleGroup
	var totalBytes int64
	skipped, missing := 0, 0
	for _, c := range library.Consoles {
		picks := selections[c.Folder]
		if len(picks) == 0 {
			continue
		}
		parts := []string{RemoteThumbnailsDir, c.System, "Named_Boxarts"}
		folder, err := server.EnsureFolder(parts, dryRun)
		if err != nil {
			return 0, err
		}
		remote := map[string]Entry{}
		if folder != "" {
			if remote, err = server.List(folder); err != nil {
				return 0, err
			}
		}
		display := folder
		if display == "" {
			display = "/" + strings.Join(parts, "/") + "/"
		}
		grp := consoleGroup{name: c.Folder}
		for _, p := range picks {
			name := library.SanitizeThumbnailName(p.Title) + ".png"
			local := filepath.Join(artworkDir, c.System, "Named_Boxarts", name)
			info, err := os.Stat(local)
			if err != nil || !info.Mode().IsRegular() {
				missing++
				continue
			}
			if existing, ok := remote[name]; ok && existing.Size != nil && *existing.Size > 0 {
				skipped++
				continue
			}
			job := uploadJob{group: c.Folder, folder: display, local: local, size: info.Size()}
			pending = append(pending, job)
			grp.jobs = append(grp.jobs, job)
			grp.bytes += info.Size()
			totalBytes += info.Size()
		}
		if len(grp.jobs) > 0 {
			groups = append(groups, grp)
		}
	}

	missingNote := ""
	if missing > 0 {
		missingNote = fmt.Sprintf(", %d missing locally", missing)
	}
	if len(pending) == 0 {
		fmt.Fprintf(out, "Artwork:   up to date (%d already present%s)\n", skipped, missingNote)
		return 0, nil
	}

	if !dryRun {
		if err := uploadJobsParallel(server, pending, workers); err != nil {
			return 0, err
		}
	}

	verb := "uploaded"
	if dryRun {
		verb = "would upload"
	}
	fmt.Fprintf(out, "Artwork:   %s %d (%s), %d already present%s\n", verb, len(pending), formatBytes(totalBytes), skipped, missingNote)
	writeGroupedUploads(out, groups, verbose)
	return len(pending), nil
}

// PrepareBezels verifies bezel PNG hashes and writes the overlay presets and per-core
// directory overrides (including DualShock 4 left-stick-to-D-pad settings).
func PrepareBezels(dir string) ([]struct{ Remote, Local string }, []struct{ Remote, Local string }, error) {
	var sources map[string]struct {
		SHA256 string `yaml:"sha256"`
	}
	if err := util.ReadYAML(filepath.Join(dir, "sources.yaml"), &sources); err != nil {
		return nil, nil, err
	}
	var assets, overrides []struct{ Remote, Local string }
	for _, c := range library.Consoles {
		image := filepath.Join(dir, c.Slug+".png")
		data, err := os.ReadFile(image)
		if err != nil {
			return nil, nil, fmt.Errorf("missing bezel PNG (run `artwork` first): %s", image)
		}
		if util.SHA256Hex(data) != strings.ToLower(sources[c.Slug].SHA256) {
			return nil, nil, fmt.Errorf("bezel PNG hash mismatch: %s", image)
		}
		preset := filepath.Join(dir, c.Slug+".cfg")
		presetText := "overlays = 1\n" +
			fmt.Sprintf("overlay0_overlay = \"%s.png\"\n", c.Slug) +
			"overlay0_full_screen = true\n" +
			"overlay0_normalized = true\n" +
			"overlay0_rect = \"0.0,0.0,1.0,1.0\"\n" +
			"overlay0_descs = 0\n"
		if err := os.WriteFile(preset, []byte(presetText), 0o644); err != nil {
			return nil, nil, err
		}
		override := filepath.Join(dir, "overrides", c.Core, c.Folder+".cfg")
		if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
			return nil, nil, err
		}
		analogMode := "0"
		comment := "# Console bezel and DualShock 4 settings for the matching ROM folder.\n# N64 has a native analog stick, so disable left-stick-to-D-pad mapping.\n"
		if c.DPadOnly {
			analogMode = "1"
			comment = "# Console bezel and DualShock 4 settings for the matching ROM folder.\n"
		}
		overrideText := comment +
			fmt.Sprintf("input_overlay = \"~/Library/Caches/RetroArch/overlays/console-bezels/%s.cfg\"\n", c.Slug) +
			"input_overlay_enable = \"true\"\n" +
			"input_overlay_enable_autopreferred = \"false\"\n" +
			"input_overlay_auto_scale = \"false\"\n" +
			"input_overlay_auto_rotate = \"false\"\n" +
			"input_overlay_opacity = \"1.000000\"\n" +
			"input_overlay_hide_when_gamepad_connected = \"false\"\n" +
			"input_overlay_hide_in_menu = \"true\"\n" +
			"input_overlay_pointer_enable = \"false\"\n" +
			fmt.Sprintf("input_player1_analog_dpad_mode = \"%s\"\n", analogMode) +
			fmt.Sprintf("input_player2_analog_dpad_mode = \"%s\"\n", analogMode)
		if c.AspectRatioIndex != "" {
			overrideText += fmt.Sprintf("aspect_ratio_index = \"%s\"\n", c.AspectRatioIndex)
		}
		if err := os.WriteFile(override, []byte(overrideText), 0o644); err != nil {
			return nil, nil, err
		}
		assets = append(assets,
			struct{ Remote, Local string }{"/" + strings.Join(RemoteOverlaysDir, "/") + "/" + c.Slug + ".png", image},
			struct{ Remote, Local string }{"/" + strings.Join(RemoteOverlaysDir, "/") + "/" + c.Slug + ".cfg", preset},
		)
		overrides = append(overrides, struct{ Remote, Local string }{"/" + RemoteConfigDir + "/" + c.Core + "/" + c.Folder + ".cfg", override})
	}
	for _, n := range []string{"LICENSE", "sources.yaml"} {
		assets = append(assets, struct{ Remote, Local string }{"/" + strings.Join(RemoteOverlaysDir, "/") + "/" + n, filepath.Join(dir, n)})
	}
	return assets, overrides, nil
}

func syncBezels(out io.Writer, server Remote, bezelDir, backupDir string, workers int, dryRun, verbose bool) (int, error) {
	assets, overrides, err := PrepareBezels(bezelDir)
	if err != nil {
		return 0, err
	}
	overlayFolder, err := server.EnsureFolder(RemoteOverlaysDir, dryRun)
	if err != nil {
		return 0, err
	}
	remoteOverlays := map[string]Entry{}
	if overlayFolder != "" {
		if remoteOverlays, err = server.List(overlayFolder); err != nil {
			return 0, err
		}
	}
	var assetJobs []uploadJob
	var assetBytes int64
	var replaceAssets []struct{ Remote, Local string }
	skipped := 0
	for _, a := range assets {
		info, err := os.Stat(a.Local)
		if err != nil {
			return 0, err
		}
		name := path.Base(a.Remote)
		if existing, ok := remoteOverlays[name]; ok {
			if existing.Size != nil && *existing.Size == info.Size() {
				skipped++
				continue
			}
			replaceAssets = append(replaceAssets, a)
			continue
		}
		display := overlayFolder
		if display == "" {
			display = "/" + strings.Join(RemoteOverlaysDir, "/") + "/"
		}
		assetJobs = append(assetJobs, uploadJob{folder: display, local: a.Local, size: info.Size()})
		assetBytes += info.Size()
	}
	if !dryRun {
		if err := uploadJobsParallel(server, assetJobs, workers); err != nil {
			return 0, err
		}
		for _, a := range replaceAssets {
			data, err := os.ReadFile(a.Local)
			if err != nil {
				return 0, err
			}
			if err := replaceRemoteFile(server, a.Remote, data, "backup"); err != nil {
				return 0, err
			}
		}
	}

	type ovChange struct {
		action string
		path   string
	}
	var ovChanges []ovChange
	for _, ov := range overrides {
		data, err := os.ReadFile(ov.Local)
		if err != nil {
			return 0, err
		}
		parts := strings.Split(strings.Trim(path.Dir(ov.Remote), "/"), "/")
		folder, err := server.EnsureFolder(parts, dryRun)
		if err != nil {
			return 0, err
		}
		var existingData []byte
		action := "add"
		if folder != "" {
			listing, err := server.List(folder)
			if err != nil {
				return 0, err
			}
			if _, ok := listing[path.Base(ov.Remote)]; ok {
				cur, err := Fetch(server, ov.Remote)
				if err != nil {
					return 0, err
				}
				if bytes.Equal(cur, data) {
					skipped++
					continue
				}
				existingData = cur
				action = "update"
			}
		}
		if !dryRun {
			if existingData != nil {
				if err := saveLocalBackup(backupDir, ov.Remote, existingData); err != nil {
					return 0, err
				}
			}
			if err := replaceRemoteFile(server, ov.Remote, data, "backup"); err != nil {
				return 0, err
			}
		}
		ovChanges = append(ovChanges, ovChange{action: action, path: ov.Remote})
	}

	assetTotal := len(assetJobs) + len(replaceAssets)
	totalChanged := assetTotal + len(ovChanges)
	if totalChanged == 0 {
		fmt.Fprintf(out, "Bezels:    up to date (%d identical)\n", skipped)
		return 0, nil
	}
	verb := "synced"
	if dryRun {
		verb = "would sync"
	}
	fmt.Fprintf(out, "Bezels:    %s %d assets, %d overrides (%d identical)\n", verb, assetTotal, len(ovChanges), skipped)
	if len(assetJobs) > 0 {
		fmt.Fprintf(out, "  assets:  +%d new (%s)\n", len(assetJobs), formatBytes(assetBytes))
		if verbose {
			for _, j := range assetJobs {
				fmt.Fprintf(out, "    + %s%s (%s)\n", j.folder, filepath.Base(j.local), formatBytes(j.size))
			}
		}
	}
	for _, a := range replaceAssets {
		fmt.Fprintf(out, "  replace  %s\n", a.Remote)
	}
	for _, ov := range ovChanges {
		fmt.Fprintf(out, "  %-8s %s\n", ov.action, ov.path)
	}
	return totalChanged, nil
}

type configFileChange struct {
	action string
	path   string
	diffs  []ConfigChange
}

func syncConfig(out io.Writer, server Remote, exportDir, configDir, backupDir string, dryRun, verbose bool) (int, error) {
	var changes []configFileChange

	// 1. Download current running /config/retroarch.cfg and back it up locally before any changes.
	mainRemote := "/" + RemoteConfigDir + "/retroarch.cfg"
	fragment, err := os.ReadFile(filepath.Join(configDir, "apple-tv.cfg"))
	if err != nil {
		return 0, err
	}
	current, err := Fetch(server, mainRemote)
	if err != nil {
		return 0, err
	}
	merged := MergeConfig(current, fragment)
	if !dryRun {
		if err := saveLocalBackup(backupDir, mainRemote, current); err != nil {
			return 0, err
		}
	}
	if !bytes.Equal(current, merged) {
		if !dryRun {
			if err := replaceRemoteFile(server, mainRemote, merged, "backup"); err != nil {
				return 0, err
			}
		}
		changes = append(changes, configFileChange{
			action: "update",
			path:   mainRemote,
			diffs:  DiffConfig(current, merged),
		})
	}

	// 2. Sync CRT shader preset config/global.slangp (backing up any existing remote copy first).
	shaderPath := filepath.Join(configDir, "global.slangp")
	if shaderData, err := os.ReadFile(shaderPath); err == nil {
		ch, err := syncSingleConfigFile(server, "/"+RemoteConfigDir+"/global.slangp", shaderData, backupDir, dryRun)
		if err != nil {
			return 0, err
		}
		if ch != nil {
			changes = append(changes, *ch)
		}
	}

	// 3. Merge Street Fighter II 6-button PCE options for both P1 & P2,
	// preserving any other existing Beetle PCE Fast core options on the device.
	pceOptPath := filepath.Join(configDir, "pce-six-button.opt")
	if optData, err := os.ReadFile(pceOptPath); err == nil {
		if selections, err := library.LoadExportSelections(exportDir); err == nil {
			for _, p := range selections["PC Engine"] {
				if !strings.Contains(strings.ToLower(p.Title), "street fighter") {
					continue
				}
				stem := strings.TrimSuffix(p.File, filepath.Ext(p.File))
				remote := "/" + RemoteConfigDir + "/Beetle PCE Fast/" + stem + ".opt"
				ch, err := mergeSingleConfigFile(server, remote, optData, backupDir, dryRun)
				if err != nil {
					return 0, err
				}
				if ch != nil {
					changes = append(changes, *ch)
				}
			}
		}
	}

	if len(changes) == 0 {
		if dryRun {
			fmt.Fprintln(out, "Config:    up to date")
		} else {
			fmt.Fprintf(out, "Config:    up to date (backup saved to %s)\n", backupDir)
		}
		return 0, nil
	}

	if dryRun {
		fmt.Fprintf(out, "Config:    would update %d file(s)\n", len(changes))
	} else {
		fmt.Fprintf(out, "Config:    updated %d file(s) (backup saved to %s)\n", len(changes), backupDir)
	}
	for _, ch := range changes {
		if len(ch.diffs) > 0 {
			fmt.Fprintf(out, "  %-6s %s (%d setting(s) changed)\n", ch.action, ch.path, len(ch.diffs))
			writeConfigDiff(out, ch.diffs, verbose)
		} else {
			fmt.Fprintf(out, "  %-6s %s\n", ch.action, ch.path)
		}
	}
	return len(changes), nil
}

func mergeSingleConfigFile(server Remote, remote string, fragment []byte, backupDir string, dryRun bool) (*configFileChange, error) {
	parts := strings.Split(strings.Trim(path.Dir(remote), "/"), "/")
	folder, err := server.EnsureFolder(parts, dryRun)
	if err != nil {
		return nil, err
	}
	var existingData []byte
	if folder != "" {
		listing, err := server.List(folder)
		if err != nil {
			return nil, err
		}
		if _, ok := listing[path.Base(remote)]; ok {
			cur, err := Fetch(server, remote)
			if err != nil {
				return nil, err
			}
			existingData = cur
		}
	}
	action := "add"
	targetData := fragment
	var diffs []ConfigChange
	if existingData != nil {
		targetData = MergeConfig(existingData, fragment)
		if bytes.Equal(existingData, targetData) {
			return nil, nil
		}
		action = "update"
		diffs = DiffConfig(existingData, targetData)
	}
	if !dryRun {
		if existingData != nil {
			if err := saveLocalBackup(backupDir, remote, existingData); err != nil {
				return nil, err
			}
		}
		if err := replaceRemoteFile(server, remote, targetData, "backup"); err != nil {
			return nil, err
		}
	}
	return &configFileChange{action: action, path: remote, diffs: diffs}, nil
}

func syncSingleConfigFile(server Remote, remote string, data []byte, backupDir string, dryRun bool) (*configFileChange, error) {
	parts := strings.Split(strings.Trim(path.Dir(remote), "/"), "/")
	folder, err := server.EnsureFolder(parts, dryRun)
	if err != nil {
		return nil, err
	}
	var existingData []byte
	action := "add"
	if folder != "" {
		listing, err := server.List(folder)
		if err != nil {
			return nil, err
		}
		if _, ok := listing[path.Base(remote)]; ok {
			cur, err := Fetch(server, remote)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(cur, data) {
				return nil, nil
			}
			existingData = cur
			action = "update"
		}
	}
	if !dryRun {
		if existingData != nil {
			if err := saveLocalBackup(backupDir, remote, existingData); err != nil {
				return nil, err
			}
		}
		if err := replaceRemoteFile(server, remote, data, "backup"); err != nil {
			return nil, err
		}
	}
	return &configFileChange{action: action, path: remote}, nil
}

func writeGroupedUploads(out io.Writer, groups []consoleGroup, verbose bool) {
	for _, g := range groups {
		fmt.Fprintf(out, "  %-14s +%d (%s)\n", g.name, len(g.jobs), formatBytes(g.bytes))
		if verbose {
			for _, j := range g.jobs {
				fmt.Fprintf(out, "    + %s%s (%s)\n", j.folder, filepath.Base(j.local), formatBytes(j.size))
			}
		}
	}
}

func writeConfigDiff(out io.Writer, diffs []ConfigChange, verbose bool) {
	limit := len(diffs)
	if !verbose && limit > maxConfigDiffLines {
		limit = maxConfigDiffLines
	}
	for _, d := range diffs[:limit] {
		if d.Added {
			fmt.Fprintf(out, "    + %s = %s\n", d.Key, d.NewVal)
		} else {
			fmt.Fprintf(out, "    ~ %s: %s -> %s\n", d.Key, d.OldVal, d.NewVal)
		}
	}
	if remaining := len(diffs) - limit; remaining > 0 {
		fmt.Fprintf(out, "    ... and %d more (use --verbose to show all)\n", remaining)
	}
}

func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func saveLocalBackup(backupDir, remotePath string, data []byte) error {
	rel := strings.TrimPrefix(remotePath, "/")
	dst := filepath.Join(backupDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// RestoreConfig uploads every file from a timestamped local config backup directory
// back to its original path on the RetroArch server.
func RestoreConfig(server Remote, backupDir string, dryRun bool) error {
	var files []struct{ local, remote string }
	err := filepath.WalkDir(backupDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(backupDir, p)
		if err != nil {
			return err
		}
		files = append(files, struct{ local, remote string }{p, "/" + filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no config files found in backup directory: %s", backupDir)
	}
	for _, f := range files {
		data, err := os.ReadFile(f.local)
		if err != nil {
			return err
		}
		if dryRun {
			fmt.Printf("Would restore %s from %s\n", f.remote, f.local)
			continue
		}
		if err := replaceRemoteFile(server, f.remote, data, "pre-restore"); err != nil {
			return err
		}
		fmt.Printf("Restored %s\n", f.remote)
	}
	if !dryRun {
		fmt.Println("Fully restart RetroArch (without saving its in-memory config) to load the restored config.")
	}
	return nil
}

// uploadJobsParallel uploads files concurrently using goroutines and verifies
// each folder's uploaded files by listing once per folder after uploads finish.
func uploadJobsParallel(server Remote, jobs []uploadJob, workers int) error {
	if len(jobs) == 0 {
		return nil
	}
	if err := runParallel(workers, len(jobs), func(_ context.Context, i int) error {
		j := jobs[i]
		if err := server.Upload(j.folder, j.local, j.size); err != nil {
			return fmt.Errorf("%s%s: %w", j.folder, filepath.Base(j.local), err)
		}
		return nil
	}); err != nil {
		return err
	}

	byFolder := map[string][]uploadJob{}
	for _, j := range jobs {
		byFolder[j.folder] = append(byFolder[j.folder], j)
	}
	for folder, list := range byFolder {
		listing, err := server.List(folder)
		if err != nil {
			return err
		}
		for _, j := range list {
			name := filepath.Base(j.local)
			if e, ok := listing[name]; !ok || e.Size == nil || *e.Size != j.size {
				return fmt.Errorf("upload verification failed: %s%s", folder, name)
			}
		}
	}
	return nil
}

func runParallel(workers, total int, fn func(ctx context.Context, i int) error) error {
	if total == 0 {
		return nil
	}
	if workers > total {
		workers = total
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := make(chan int, total)
	for i := range total {
		ch <- i
	}
	close(ch)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				if ctx.Err() != nil {
					return
				}
				if err := fn(ctx, i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}
