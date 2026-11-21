package device

import (
	"bytes"
	"context"
	"fmt"
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

// RemoteROMRoot is where RetroArch keeps imported ROMs on the Apple TV.
var RemoteROMRoot = []string{"downloads", "ROMS"}

// SyncOptions controls what gets synced from local to the remote RetroArch server.
type SyncOptions struct {
	ExportDir  string
	ArtworkDir string
	BezelDir   string
	ConfigDir  string
	StateDir   string
	Workers    int
	DryRun     bool
	ROMs       bool
	Playlists  bool
	Artwork    bool
	Bezels     bool
	Config     bool
}

type uploadJob struct {
	folder string
	local  string
	size   int64
	sha256 string // optional pre-upload local SHA-256 check
}

// SyncAll uploads ROMs, playlists, artwork, bezels, and configuration to the RetroArch
// web file server, using parallel goroutines for bulk transfers.
func SyncAll(server Remote, o SyncOptions) error {
	if o.Workers < 1 {
		o.Workers = 4
	}
	if o.ROMs {
		if err := syncROMs(server, o.ExportDir, o.Workers, o.DryRun); err != nil {
			return fmt.Errorf("sync roms: %w", err)
		}
	}
	if o.Playlists {
		if err := syncPlaylists(server, o.ExportDir, o.DryRun); err != nil {
			return fmt.Errorf("sync playlists: %w", err)
		}
	}
	if o.Artwork {
		if err := syncArtwork(server, o.ExportDir, o.ArtworkDir, o.Workers, o.DryRun); err != nil {
			return fmt.Errorf("sync artwork: %w", err)
		}
	}
	if o.Bezels {
		if err := syncBezels(server, o.BezelDir, o.Workers, o.DryRun); err != nil {
			return fmt.Errorf("sync bezels: %w", err)
		}
	}
	if o.Config {
		if err := syncConfig(server, o.ExportDir, o.ConfigDir, o.StateDir, o.DryRun); err != nil {
			return fmt.Errorf("sync config: %w", err)
		}
	}
	return nil
}

func syncROMs(server Remote, exportDir string, workers int, dryRun bool) error {
	selections, err := library.LoadExportSelections(exportDir)
	if err != nil {
		return err
	}
	consoles := make([]string, 0, len(selections))
	for c := range selections {
		consoles = append(consoles, c)
	}
	sort.Strings(consoles)

	var pending []uploadJob
	skipped := 0
	for _, console := range consoles {
		picks := selections[console]
		if len(picks) == 0 {
			continue
		}
		parts := append(append([]string{}, RemoteROMRoot...), console)
		folder, err := server.EnsureFolder(parts, dryRun)
		if err != nil {
			return err
		}
		remote := map[string]Entry{}
		if folder != "" {
			if remote, err = server.List(folder); err != nil {
				return err
			}
		}
		display := folder
		if display == "" {
			display = "/" + strings.Join(parts, "/") + "/"
		}
		for _, p := range picks {
			if !util.SafeName(p.File) {
				return fmt.Errorf("invalid ROM name: %q", p.File)
			}
			local := filepath.Join(exportDir, console, p.File)
			info, err := os.Lstat(local)
			if err != nil || !info.Mode().IsRegular() || info.Size() != p.Bytes {
				return fmt.Errorf("local ROM does not match manifest: %s", local)
			}
			existing, ok := remote[p.File]
			switch {
			case !ok:
				pending = append(pending, uploadJob{display, local, p.Bytes, p.ExportSHA256})
			case existing.Size != nil && *existing.Size == p.Bytes:
				skipped++
			default:
				return fmt.Errorf("remote file exists with a different size or type: %s%s", display, p.File)
			}
		}
	}

	if dryRun {
		for _, j := range pending {
			fmt.Printf("Would upload ROM %s%s\n", j.folder, filepath.Base(j.local))
		}
		fmt.Printf("ROMs: would upload %d, skip %d already present.\n", len(pending), skipped)
		return nil
	}

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
		return err
	}

	if err := uploadJobsParallel(server, pending, workers, "ROM"); err != nil {
		return err
	}
	fmt.Printf("ROMs: uploaded %d, skipped %d already present.\n", len(pending), skipped)
	return nil
}

func syncPlaylists(server Remote, exportDir string, dryRun bool) error {
	plDir := filepath.Join(exportDir, "playlists")
	entries, err := os.ReadDir(plDir)
	if err != nil {
		return fmt.Errorf("read playlists in %s (run `build` first): %w", plDir, err)
	}
	folder, err := server.EnsureFolder([]string{"playlists"}, dryRun)
	if err != nil {
		return err
	}
	remote := map[string]Entry{}
	if folder != "" {
		if remote, err = server.List(folder); err != nil {
			return err
		}
	}
	updated, skipped := 0, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lpl") {
			continue
		}
		local := filepath.Join(plDir, e.Name())
		data, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		remotePath := "/playlists/" + e.Name()
		if _, exists := remote[e.Name()]; exists {
			cur, err := Fetch(server, remotePath)
			if err != nil {
				return err
			}
			if bytes.Equal(cur, data) {
				skipped++
				continue
			}
		}
		if dryRun {
			fmt.Printf("Would sync playlist %s\n", remotePath)
			updated++
			continue
		}
		if err := replaceRemoteFile(server, remotePath, data, "backup"); err != nil {
			return err
		}
		fmt.Printf("Synced playlist %s\n", remotePath)
		updated++
	}
	verb := "synced"
	if dryRun {
		verb = "would sync"
	}
	fmt.Printf("Playlists: %s %d, skipped %d identical.\n", verb, updated, skipped)
	return nil
}

func syncArtwork(server Remote, exportDir, artworkDir string, workers int, dryRun bool) error {
	selections, err := library.LoadExportSelections(exportDir)
	if err != nil {
		return err
	}
	var pending []uploadJob
	skipped, missing := 0, 0
	for _, c := range library.Consoles {
		picks := selections[c.Folder]
		if len(picks) == 0 {
			continue
		}
		parts := []string{"thumbnails", c.System, "Named_Boxarts"}
		folder, err := server.EnsureFolder(parts, dryRun)
		if err != nil {
			return err
		}
		remote := map[string]Entry{}
		if folder != "" {
			if remote, err = server.List(folder); err != nil {
				return err
			}
		}
		display := folder
		if display == "" {
			display = "/" + strings.Join(parts, "/") + "/"
		}
		for _, p := range picks {
			name := library.SanitizeThumbnailName(p.Title) + ".png"
			local := filepath.Join(artworkDir, c.System, "Named_Boxarts", name)
			info, err := os.Stat(local)
			if err != nil || !info.Mode().IsRegular() {
				missing++
				continue
			}
			if existing, ok := remote[name]; ok && existing.Size != nil && *existing.Size == info.Size() {
				skipped++
				continue
			}
			pending = append(pending, uploadJob{folder: display, local: local, size: info.Size()})
		}
	}

	if dryRun {
		for _, j := range pending {
			fmt.Printf("Would upload artwork %s%s\n", j.folder, filepath.Base(j.local))
		}
		fmt.Printf("Artwork: would upload %d, skip %d identical, %d missing locally.\n", len(pending), skipped, missing)
		return nil
	}

	if err := uploadJobsParallel(server, pending, workers, "artwork"); err != nil {
		return err
	}
	fmt.Printf("Artwork: uploaded %d, skipped %d identical, %d missing locally.\n", len(pending), skipped, missing)
	return nil
}

// PrepareBezels verifies bezel PNG hashes and writes the overlay presets and per-core
// directory overrides (including DualShock 4 left-stick-to-D-pad settings).
func PrepareBezels(dir string) ([]struct{ Remote, Local string }, []struct{ Remote, Local string }, error) {
	var sources map[string]struct {
		SHA256 string `json:"sha256"`
	}
	if err := util.ReadJSON(filepath.Join(dir, "sources.json"), &sources); err != nil {
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
		if err := os.WriteFile(override, []byte(overrideText), 0o644); err != nil {
			return nil, nil, err
		}
		assets = append(assets,
			struct{ Remote, Local string }{"/overlays/console-bezels/" + c.Slug + ".png", image},
			struct{ Remote, Local string }{"/overlays/console-bezels/" + c.Slug + ".cfg", preset},
		)
		overrides = append(overrides, struct{ Remote, Local string }{"/config/" + c.Core + "/" + c.Folder + ".cfg", override})
	}
	for _, n := range []string{"LICENSE", "sources.json"} {
		assets = append(assets, struct{ Remote, Local string }{"/overlays/console-bezels/" + n, filepath.Join(dir, n)})
	}
	return assets, overrides, nil
}

func syncBezels(server Remote, bezelDir string, workers int, dryRun bool) error {
	assets, overrides, err := PrepareBezels(bezelDir)
	if err != nil {
		return err
	}
	overlayFolder, err := server.EnsureFolder([]string{"overlays", "console-bezels"}, dryRun)
	if err != nil {
		return err
	}
	remoteOverlays := map[string]Entry{}
	if overlayFolder != "" {
		if remoteOverlays, err = server.List(overlayFolder); err != nil {
			return err
		}
	}
	var assetJobs []uploadJob
	skipped := 0
	for _, a := range assets {
		info, err := os.Stat(a.Local)
		if err != nil {
			return err
		}
		name := path.Base(a.Remote)
		if existing, ok := remoteOverlays[name]; ok && existing.Size != nil && *existing.Size == info.Size() {
			skipped++
			continue
		}
		display := overlayFolder
		if display == "" {
			display = "/overlays/console-bezels/"
		}
		assetJobs = append(assetJobs, uploadJob{folder: display, local: a.Local, size: info.Size()})
	}
	if dryRun {
		for _, j := range assetJobs {
			fmt.Printf("Would upload bezel asset %s%s\n", j.folder, filepath.Base(j.local))
		}
	} else if err := uploadJobsParallel(server, assetJobs, workers, "bezel"); err != nil {
		return err
	}

	ovUpdated := 0
	for _, ov := range overrides {
		data, err := os.ReadFile(ov.Local)
		if err != nil {
			return err
		}
		parts := strings.Split(strings.Trim(path.Dir(ov.Remote), "/"), "/")
		folder, err := server.EnsureFolder(parts, dryRun)
		if err != nil {
			return err
		}
		if folder != "" {
			listing, err := server.List(folder)
			if err != nil {
				return err
			}
			if _, ok := listing[path.Base(ov.Remote)]; ok {
				cur, err := Fetch(server, ov.Remote)
				if err != nil {
					return err
				}
				if bytes.Equal(cur, data) {
					skipped++
					continue
				}
			}
		}
		if dryRun {
			fmt.Printf("Would sync override %s\n", ov.Remote)
			ovUpdated++
			continue
		}
		if err := replaceRemoteFile(server, ov.Remote, data, "backup"); err != nil {
			return err
		}
		fmt.Printf("Synced override %s\n", ov.Remote)
		ovUpdated++
	}
	verb := "synced"
	if dryRun {
		verb = "would sync"
	}
	fmt.Printf("Bezels: %s %d assets and %d overrides, skipped %d identical.\n", verb, len(assetJobs), ovUpdated, skipped)
	return nil
}

func syncConfig(server Remote, exportDir, configDir, stateDir string, dryRun bool) error {
	// 1. Merge config/apple-tv.cfg into remote /config/retroarch.cfg.
	fragment, err := os.ReadFile(filepath.Join(configDir, "apple-tv.cfg"))
	if err != nil {
		return err
	}
	current, err := Fetch(server, "/"+mainConfig)
	if err != nil {
		return err
	}
	merged := MergeConfig(current, fragment)
	if bytes.Equal(current, merged) {
		fmt.Println("Config: /config/retroarch.cfg already up to date.")
	} else if dryRun {
		fmt.Printf("Would update /%s (%d -> %d bytes), keeping a backup.\n", mainConfig, len(current), len(merged))
	} else {
		backupDir := filepath.Join(stateDir, "config-backups")
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return err
		}
		backup := filepath.Join(backupDir, "retroarch-"+time.Now().UTC().Format("20060102T150405Z")+".cfg")
		if err := os.WriteFile(backup, current, 0o644); err != nil {
			return err
		}
		if err := replaceRemoteFile(server, "/"+mainConfig, merged, "backup"); err != nil {
			return err
		}
		fmt.Printf("Config: updated /%s (local backup: %s)\n", mainConfig, backup)
		fmt.Println("Fully restart RetroArch (without saving its in-memory config) to load the updated config.")
	}

	// 2. Sync CRT shader preset config/global.slangp.
	shaderPath := filepath.Join(configDir, "global.slangp")
	if shaderData, err := os.ReadFile(shaderPath); err == nil {
		if err := syncSingleConfigFile(server, "/config/global.slangp", shaderData, dryRun); err != nil {
			return err
		}
	}

	// 3. Sync Street Fighter II 6-button PCE options for both P1 & P2.
	pceOptPath := filepath.Join(configDir, "pce-six-button.opt")
	if optData, err := os.ReadFile(pceOptPath); err == nil {
		if selections, err := library.LoadExportSelections(exportDir); err == nil {
			for _, p := range selections["PC Engine"] {
				if !strings.Contains(strings.ToLower(p.Title), "street fighter") {
					continue
				}
				stem := strings.TrimSuffix(p.File, filepath.Ext(p.File))
				remote := "/config/Beetle PCE Fast/" + stem + ".opt"
				if err := syncSingleConfigFile(server, remote, optData, dryRun); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func syncSingleConfigFile(server Remote, remote string, data []byte, dryRun bool) error {
	parts := strings.Split(strings.Trim(path.Dir(remote), "/"), "/")
	folder, err := server.EnsureFolder(parts, dryRun)
	if err != nil {
		return err
	}
	if folder != "" {
		listing, err := server.List(folder)
		if err != nil {
			return err
		}
		if _, ok := listing[path.Base(remote)]; ok {
			cur, err := Fetch(server, remote)
			if err != nil {
				return err
			}
			if bytes.Equal(cur, data) {
				return nil
			}
		}
	}
	if dryRun {
		fmt.Printf("Would sync %s\n", remote)
		return nil
	}
	if err := replaceRemoteFile(server, remote, data, "backup"); err != nil {
		return err
	}
	fmt.Printf("Synced %s\n", remote)
	return nil
}

// uploadJobsParallel uploads files concurrently using goroutines and verifies
// each folder's uploaded files by listing once per folder after uploads finish.
func uploadJobsParallel(server Remote, jobs []uploadJob, workers int, label string) error {
	if len(jobs) == 0 {
		return nil
	}
	if err := runParallel(workers, len(jobs), func(_ context.Context, i int) error {
		j := jobs[i]
		if err := server.Upload(j.folder, j.local, j.size); err != nil {
			return fmt.Errorf("%s%s: %w", j.folder, filepath.Base(j.local), err)
		}
		fmt.Printf("Uploaded %s %s%s\n", label, j.folder, filepath.Base(j.local))
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
