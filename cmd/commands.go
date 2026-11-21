package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/fabiogoma/retroarch/internal/artwork"
	"github.com/fabiogoma/retroarch/internal/device"
	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/repocheck"
)

func server(c *cobra.Command) device.Remote { return device.NewServer(serverURL(c)) }

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Scan local ROMs and build Top 10 through Top 100 export folders with playlists",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		topSpec := viper.GetString("top")
		if c.Flags().Changed("top") {
			topSpec, _ = c.Flags().GetString("top")
		}
		counts, err := library.ParseCounts(topSpec)
		if err != nil {
			return err
		}
		unzip := viper.GetBool("unzip")
		if c.Flags().Changed("unzip") {
			unzip, _ = c.Flags().GetBool("unzip")
		}
		fromCache, _ := c.Flags().GetBool("from-cache")
		return library.Build(library.BuildOptions{
			DataDir:    configDir(),
			StateDir:   stateDir(),
			ROMsDir:    romsDir(c),
			Counts:     counts,
			Unzip:      unzip,
			FromCache:  fromCache,
			DeviceHome: viper.GetString("device_home"),
		})
	},
}

var artworkCmd = &cobra.Command{
	Use:   "artwork",
	Short: "Download box art and console bezel PNGs locally in parallel for an export",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		export, _ := c.Flags().GetString("export")
		top := viper.GetInt("sync_top")
		if c.Flags().Changed("top") {
			top, _ = c.Flags().GetInt("top")
		}
		dry, _ := c.Flags().GetBool("dry-run")
		if export == "" {
			export = exportDirFor(c, top)
		}
		return artwork.Download(artwork.Options{
			ExportDir:  export,
			ArtworkDir: artworkDir(),
			BezelDir:   bezelDir(),
			ConfigDir:  configDir(),
			Workers:    workersFor(c, "artwork_workers"),
			DryRun:     dry,
		})
	},
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync ROMs, playlists, artwork, bezels, and DualShock 4 config to RetroArch in parallel",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		dry, _ := c.Flags().GetBool("dry-run")
		if restoreDir, _ := c.Flags().GetString("restore-config"); restoreDir != "" {
			return device.RestoreConfig(server(c), restoreDir, dry)
		}
		export, _ := c.Flags().GetString("export")
		top := viper.GetInt("sync_top")
		if c.Flags().Changed("top") {
			top, _ = c.Flags().GetInt("top")
		}
		only, _ := c.Flags().GetString("only")
		verbose, _ := c.Flags().GetBool("verbose")
		if export == "" {
			export = exportDirFor(c, top)
		}
		opts := device.SyncOptions{
			ExportDir:  export,
			ArtworkDir: artworkDir(),
			BezelDir:   bezelDir(),
			ConfigDir:  configDir(),
			StateDir:   stateDir(),
			BackupDir:  backupDir(),
			Workers:    workersFor(c, "sync_workers"),
			DryRun:     dry,
			Verbose:    verbose,
		}
		if err := applyOnlyFilter(&opts, only); err != nil {
			return err
		}
		return device.SyncAll(server(c), opts)
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
	buildCmd.Flags().String("top", "", "comma-separated top counts to generate (overrides top in config)")
	buildCmd.Flags().Bool("unzip", false, "extract single-ROM ZIP archives into the export (overrides unzip in config)")
	buildCmd.Flags().Bool("from-cache", false, "reuse state/scan-cache.json without rescanning ROMs/")

	artworkCmd.Flags().Int("top", 0, "export set to fetch artwork for (overrides sync_top in config)")
	artworkCmd.Flags().String("export", "", "explicit export folder path (overrides --top)")
	artworkCmd.Flags().Bool("dry-run", false, "preview downloads without writing files")

	syncCmd.Flags().Int("top", 0, "export set to sync (overrides sync_top in config)")
	syncCmd.Flags().String("export", "", "explicit export folder path (overrides --top)")
	syncCmd.Flags().String("only", "", "comma-separated subset to sync: roms,playlists,artwork,bezels,config (default: all)")
	syncCmd.Flags().String("restore-config", "", "restore a backed-up config folder to the RetroArch server")
	syncCmd.Flags().Bool("dry-run", false, "preview without uploading")
	syncCmd.Flags().BoolP("verbose", "v", false, "show per-file upload details")
}
