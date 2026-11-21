package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/fabiogoma/retroarch/internal/artwork"
	"github.com/fabiogoma/retroarch/internal/device"
	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/repocheck"
)

func server() device.Remote { return device.NewServer(viper.GetString("server")) }

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Scan local ROMs and build Top 30, Top 40, and Top 50 export folders with playlists",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		topSpec, _ := c.Flags().GetString("top")
		counts, err := library.ParseCounts(topSpec)
		if err != nil {
			return err
		}
		unzip, _ := c.Flags().GetBool("unzip")
		fromCache, _ := c.Flags().GetBool("from-cache")
		return library.Build(library.BuildOptions{
			DataDir:    dataDir(),
			ROMsDir:    romsDir(),
			Counts:     counts,
			Unzip:      unzip,
			FromCache:  fromCache,
			DeviceHome: viper.GetString("device_home"),
		})
	},
}

var artworkCmd = &cobra.Command{
	Use:   "artwork",
	Short: "Download box art and console bezel PNGs locally for an export",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		export, _ := c.Flags().GetString("export")
		top, _ := c.Flags().GetInt("top")
		dry, _ := c.Flags().GetBool("dry-run")
		if export == "" {
			export = exportDirFor(top)
		}
		return artwork.Download(artwork.Options{
			ExportDir:  export,
			ArtworkDir: artworkDir(),
			BezelDir:   filepath.Join(dataDir(), "bezels"),
			Workers:    viper.GetInt("workers"),
			DryRun:     dry,
		})
	},
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync ROMs, playlists, artwork, bezels, and DualShock 4 config to RetroArch in parallel",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		export, _ := c.Flags().GetString("export")
		top, _ := c.Flags().GetInt("top")
		dry, _ := c.Flags().GetBool("dry-run")
		only, _ := c.Flags().GetString("only")
		if export == "" {
			export = exportDirFor(top)
		}
		opts := device.SyncOptions{
			ExportDir:  export,
			ArtworkDir: artworkDir(),
			BezelDir:   filepath.Join(dataDir(), "bezels"),
			ConfigDir:  configDir(),
			StateDir:   filepath.Join(dataDir(), "state"),
			Workers:    viper.GetInt("workers"),
			DryRun:     dry,
		}
		if err := applyOnlyFilter(&opts, only); err != nil {
			return err
		}
		return device.SyncAll(server(), opts)
	},
}

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify no ROMs, artwork, or runtime state are tracked in Git",
	Args:  cobra.NoArgs,
	RunE:  func(*cobra.Command, []string) error { return repocheck.Check(projectRoot()) },
}

func applyOnlyFilter(o *device.SyncOptions, spec string) error {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" || spec == "all" {
		o.ROMs, o.Playlists, o.Artwork, o.Bezels, o.Config = true, true, true, true, true
		return nil
	}
	for _, part := range strings.Split(spec, ",") {
		switch strings.TrimSpace(part) {
		case "":
		case "roms":
			o.ROMs = true
		case "playlists":
			o.Playlists = true
		case "artwork":
			o.Artwork = true
		case "bezels":
			o.Bezels = true
		case "config":
			o.Config = true
		default:
			return fmt.Errorf("unknown --only item %q (valid: roms, playlists, artwork, bezels, config)", part)
		}
	}
	return nil
}

func init() {
	buildCmd.Flags().String("top", defaults["top"].(string), "comma-separated top counts to generate (e.g. 30, 40, 50, or 30,40,50)")
	buildCmd.Flags().Bool("unzip", false, "extract single-ROM ZIP archives into the export")
	buildCmd.Flags().Bool("from-cache", false, "reuse rom_library/state/scan-cache.json without rescanning ROMs/")

	artworkCmd.Flags().Int("top", defaults["sync_top"].(int), "export set to fetch artwork for (30, 40, or 50)")
	artworkCmd.Flags().String("export", "", "explicit export folder path (overrides --top)")
	artworkCmd.Flags().Bool("dry-run", false, "preview downloads without writing files")

	syncCmd.Flags().Int("top", defaults["sync_top"].(int), "export set to sync (30, 40, or 50)")
	syncCmd.Flags().String("export", "", "explicit export folder path (overrides --top)")
	syncCmd.Flags().String("only", "", "comma-separated subset to sync: roms,playlists,artwork,bezels,config (default: all)")
	syncCmd.Flags().Bool("dry-run", false, "preview without uploading")
}
