package library

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/fabiogoma/retroarch/internal/util"
)

// PlaylistNames maps each console folder to its RetroArch playlist/system name.
var PlaylistNames = []struct{ Console, System string }{
	{"nes", "Nintendo - Nintendo Entertainment System"},
	{"snes", "Nintendo - Super Nintendo Entertainment System"},
	{"n64", "Nintendo - Nintendo 64"},
	{"genesis", "Sega - Mega Drive - Genesis"},
	{"master system", "Sega - Master System - Mark III"},
	{"PC Engine", "NEC - PC Engine - TurboGrafx 16"},
}

type playlistItem struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	CorePath string `json:"core_path"`
	CoreName string `json:"core_name"`
	CRC32    string `json:"crc32"`
	DBName   string `json:"db_name"`
}

type playlist struct {
	Version         string         `json:"version"`
	DefaultCorePath string         `json:"default_core_path"`
	DefaultCoreName string         `json:"default_core_name"`
	Items           []playlistItem `json:"items"`
}

// GeneratePlaylists writes RetroArch playlists from an export's manifests.
func GeneratePlaylists(dataDir, export, output, deviceHome string) error {
	home := deviceHome
	if home != "~" {
		if !strings.HasPrefix(home, "/") {
			return fmt.Errorf("--device-home must be ~ or an absolute app-container path")
		}
		home = path.Clean(home)
	}
	for _, part := range strings.Split(deviceHome, "/") {
		if part == ".." {
			return fmt.Errorf("--device-home must be ~ or an absolute app-container path")
		}
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	defaultsFile := filepath.Join(dataDir, "state", "apple-tv-core-defaults.json")
	if _, err := os.Stat(defaultsFile); err != nil {
		defaultsFile = filepath.Join(dataDir, "apple_tv_cores.json")
	}
	var cores []struct {
		System   string `json:"system"`
		CorePath string `json:"core_path"`
		CoreName string `json:"core_name"`
	}
	if err := util.ReadJSON(defaultsFile, &cores); err != nil {
		return err
	}
	report := map[string]int{}
	for _, p := range PlaylistNames {
		var entries []Selection
		if err := util.ReadJSON(filepath.Join(export, p.Console, "selection.json"), &entries); err != nil {
			return err
		}
		corePath, coreName := "DETECT", "DETECT"
		for _, c := range cores {
			if c.System == p.System {
				if c.CorePath != "" {
					corePath = c.CorePath
				}
				if c.CoreName != "" {
					coreName = c.CoreName
				}
			}
		}
		pl := playlist{Version: "1.5", DefaultCorePath: corePath, DefaultCoreName: coreName, Items: []playlistItem{}}
		for _, e := range entries {
			item := path.Join(home, "Library/Caches/RetroArch/downloads/ROMS", p.Console, e.File)
			if strings.HasSuffix(strings.ToLower(e.File), ".zip") && e.ArchiveMember != "" {
				item += "#" + e.ArchiveMember
			}
			pl.Items = append(pl.Items, playlistItem{item, e.Title, corePath, coreName, "DETECT", p.System + ".lpl"})
		}
		data, err := util.MarshalIndent(pl)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(output, p.System+".lpl"), data, 0o644); err != nil {
			return err
		}
		report[p.System] = len(pl.Items)
	}
	gen := map[string]any{"device_home": home, "playlists": report,
		"note": "Apple RetroArch expands the home alias when loading playlists. Core paths reference framework bundles. Playback must be verified on the Apple TV."}
	if err := util.WriteJSON(filepath.Join(output, "generation.json"), gen); err != nil {
		return err
	}
	out, _ := util.MarshalIndent(report)
	fmt.Print(string(out))
	return nil
}
