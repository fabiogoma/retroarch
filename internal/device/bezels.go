package device

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

// Console describes how a console maps to its ROM folder, core and playlist.
type Console struct {
	Slug, Folder, Core, System string
}

// Consoles lists every console that gets a bezel, in install order.
var Consoles = []Console{
	{"nes", "nes", "Nestopia", "Nintendo - Nintendo Entertainment System"},
	{"snes", "snes", "Snes9x", "Nintendo - Super Nintendo Entertainment System"},
	{"genesis", "genesis", "Genesis Plus GX", "Sega - Mega Drive - Genesis"},
	{"master-system", "master system", "Genesis Plus GX", "Sega - Master System - Mark III"},
	{"pc-engine", "PC Engine", "Beetle PCE Fast", "NEC - PC Engine - TurboGrafx 16"},
	{"n64", "n64", "Mupen64Plus-Next", "Nintendo - Nintendo 64"},
}

type bezelFile struct{ Remote, Local string }

// PrepareBezels verifies the artwork and writes the presets and per-core overrides.
// Artwork and presets come first and the config overrides that enable them last.
func PrepareBezels(dir string) ([]bezelFile, error) {
	var sources map[string]struct {
		SHA256 string `json:"sha256"`
	}
	if err := util.ReadJSON(filepath.Join(dir, "sources.json"), &sources); err != nil {
		return nil, err
	}
	var files, overrides []bezelFile
	for _, c := range Consoles {
		image := filepath.Join(dir, c.Slug+".png")
		data, err := os.ReadFile(image)
		if err != nil {
			return nil, fmt.Errorf("Missing artwork; run `download bezels` first: %s", image)
		}
		if util.SHA256Hex(data) != sources[c.Slug].SHA256 {
			return nil, fmt.Errorf("Artwork hash mismatch: %s", image)
		}
		preset := filepath.Join(dir, c.Slug+".cfg")
		presetText := "overlays = 1\n" +
			fmt.Sprintf("overlay0_overlay = \"%s.png\"\n", c.Slug) +
			"overlay0_full_screen = true\n" +
			"overlay0_normalized = true\n" +
			"overlay0_rect = \"0.0,0.0,1.0,1.0\"\n" +
			"overlay0_descs = 0\n"
		if err := os.WriteFile(preset, []byte(presetText), 0o644); err != nil {
			return nil, err
		}
		override := filepath.Join(dir, "overrides", c.Core, c.Folder+".cfg")
		if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
			return nil, err
		}
		overrideText := "# Console bezel for the matching ROM folder.\n" +
			fmt.Sprintf("input_overlay = \"~/Library/Caches/RetroArch/overlays/console-bezels/%s.cfg\"\n", c.Slug) +
			"input_overlay_enable = \"true\"\n" +
			"input_overlay_enable_autopreferred = \"false\"\n" +
			"input_overlay_auto_scale = \"false\"\n" +
			"input_overlay_auto_rotate = \"false\"\n" +
			"input_overlay_opacity = \"1.000000\"\n" +
			"input_overlay_hide_when_gamepad_connected = \"false\"\n" +
			"input_overlay_hide_in_menu = \"true\"\n" +
			"input_overlay_pointer_enable = \"false\"\n"
		if err := os.WriteFile(override, []byte(overrideText), 0o644); err != nil {
			return nil, err
		}
		files = append(files, bezelFile{"/overlays/console-bezels/" + c.Slug + ".png", image},
			bezelFile{"/overlays/console-bezels/" + c.Slug + ".cfg", preset})
		overrides = append(overrides, bezelFile{"/config/" + c.Core + "/" + c.Folder + ".cfg", override})
	}
	for _, n := range []string{"LICENSE", "sources.json"} {
		files = append(files, bezelFile{"/overlays/console-bezels/" + n, filepath.Join(dir, n)})
	}
	return append(files, overrides...), nil
}

var (
	autoOverrides = regexp.MustCompile(`(?m)^auto_overrides_enable\s*=\s*"true"`)
	aspectIndex   = regexp.MustCompile(`(?m)^aspect_ratio_index\s*=\s*"0"`)
)

// InstallBezels previews (default) or uploads missing console bezel files.
// Existing remote files that differ are never overwritten.
func InstallBezels(server Remote, bezelDir, stateDir string, install bool) error {
	files, err := PrepareBezels(bezelDir)
	if err != nil {
		return err
	}
	config, err := Fetch(server, "/"+mainConfig)
	if err != nil {
		return err
	}
	if !autoOverrides.Match(config) {
		return fmt.Errorf("Automatic config overrides must be enabled first.")
	}
	if !aspectIndex.Match(config) {
		return fmt.Errorf("These bezels require the existing 4:3 display setting.")
	}
	known := map[string]bool{}
	for _, f := range files {
		known[f.Remote] = true
	}
	counts := map[string]int{}
	for _, c := range Consoles {
		folder := "/config/" + c.Core + "/"
		listing, err := server.List(folder)
		if err != nil {
			return err
		}
		var unexpected []string
		for n := range listing {
			if strings.HasSuffix(n, ".cfg") && !known[folder+n] {
				unexpected = append(unexpected, n)
			}
		}
		if len(unexpected) > 0 {
			sort.Strings(unexpected)
			return fmt.Errorf("Check existing overrides before installing: %s: %v", c.Core, unexpected)
		}
		raw, err := Fetch(server, "/playlists/"+c.System+".lpl")
		if err != nil {
			return err
		}
		var playlist struct {
			Items []struct {
				Path string `json:"path"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &playlist); err != nil {
			return err
		}
		for _, item := range playlist.Items {
			rom := strings.SplitN(item.Path, "#", 2)[0]
			if path.Base(path.Dir(rom)) != c.Folder {
				return fmt.Errorf("Unexpected ROM folder: %s", item.Path)
			}
		}
		counts[c.Folder] = len(playlist.Items)
	}

	var pending []bezelFile
	for _, f := range files {
		parts := strings.Split(strings.Trim(path.Dir(f.Remote), "/"), "/")
		available, err := server.EnsureFolder(parts, true)
		if err != nil {
			return err
		}
		exists := false
		if available != "" {
			listing, err := server.List(available)
			if err != nil {
				return err
			}
			_, exists = listing[path.Base(f.Remote)]
		}
		if !exists {
			pending = append(pending, f)
			continue
		}
		remote, err := Fetch(server, f.Remote)
		if err != nil {
			return err
		}
		local, err := os.ReadFile(f.Local)
		if err != nil {
			return err
		}
		if !bytes.Equal(remote, local) {
			return fmt.Errorf("Refusing to overwrite different remote file: %s", f.Remote)
		}
	}
	cj, _ := json.Marshal(counts)
	fmt.Println("Playlist coverage:", string(cj))
	fmt.Printf("%d new files; %d already identical.\n", len(pending), len(files)-len(pending))
	if !install {
		for _, f := range pending {
			fmt.Println("Would upload", f.Remote)
		}
		return nil
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	backup := filepath.Join(stateDir, "retroarch-before-install-"+stamp+".cfg")
	if err := os.WriteFile(backup, config, 0o644); err != nil {
		return err
	}
	report := map[string]any{
		"server": server.URL(), "started_utc": stamp, "installed": false, "backup": backup,
		"main_config_sha256": util.SHA256Hex(config), "playlist_games": counts,
		"files": map[string]string{}, "visual_playback_verified": false,
		"scope": "Folder overrides for the six currently configured console cores.",
	}
	reportPath := filepath.Join(stateDir, "installation.json")
	if err := util.WriteJSON(reportPath, report); err != nil {
		return err
	}
	hashes := report["files"].(map[string]string)
	verify := func(f bezelFile, msg string) ([]byte, error) {
		local, err := os.ReadFile(f.Local)
		if err != nil {
			return nil, err
		}
		remote, err := Fetch(server, f.Remote)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(remote, local) {
			return nil, fmt.Errorf("%s: %s", msg, f.Remote)
		}
		return local, nil
	}
	for _, f := range pending {
		folder, err := server.EnsureFolder(strings.Split(strings.Trim(path.Dir(f.Remote), "/"), "/"), false)
		if err != nil {
			return err
		}
		info, err := os.Stat(f.Local)
		if err != nil {
			return err
		}
		if err := server.Upload(folder, f.Local, info.Size()); err != nil {
			return err
		}
		local, err := verify(f, "Upload verification failed")
		if err != nil {
			return err
		}
		fmt.Println("Uploaded and verified", f.Remote)
		hashes[f.Remote] = util.SHA256Hex(local)
		if err := util.WriteJSON(reportPath, report); err != nil {
			return err
		}
	}
	for _, f := range files {
		local, err := verify(f, "Final verification failed")
		if err != nil {
			return err
		}
		hashes[f.Remote] = util.SHA256Hex(local)
	}
	after, err := Fetch(server, "/"+mainConfig)
	if err != nil {
		return err
	}
	if !bytes.Equal(after, config) {
		return fmt.Errorf("Main config changed during installation; inspect before continuing.")
	}
	report["installed"] = true
	report["main_config_unchanged"] = true
	report["finished_utc"] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := util.WriteJSON(reportPath, report); err != nil {
		return err
	}
	fmt.Println("All six console bezels installed. Reload content to activate.")
	return nil
}
