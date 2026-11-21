package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/fabiogoma/retroarch/internal/device"
	"github.com/fabiogoma/retroarch/internal/fetch"
	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/repocheck"
)

func server() device.Remote { return device.NewServer(viper.GetString("server")) }

var catalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Scan the ROM folders and build the curated Apple TV export",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		count := viper.GetInt("count")
		unzip, _ := c.Flags().GetBool("unzip-export")
		fromCatalog, _ := c.Flags().GetBool("from-catalog")
		return library.RunCatalog(dataDir(), romsDir(), count, unzip, fromCatalog)
	},
}

var playlistsCmd = &cobra.Command{
	Use:   "playlists",
	Short: "Generate RetroArch playlists from an export's manifests",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		export, _ := c.Flags().GetString("export")
		output, _ := c.Flags().GetString("output")
		home := viper.GetString("device_home")
		if export == "" {
			export = defaultExport()
		}
		if output == "" {
			output = filepath.Join(dataDir(), "state", "apple-tv-playlists")
		}
		return library.GeneratePlaylists(dataDir(), export, output, home)
	},
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Additively upload an export's ROMs to the Apple TV (never deletes or overwrites)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		export, _ := c.Flags().GetString("export")
		dry, _ := c.Flags().GetBool("dry-run")
		if export == "" {
			export = defaultExport()
		}
		groups, err := device.LoadExport(export)
		if err != nil {
			return err
		}
		return device.Sync(server(), groups, dry)
	},
}

var profileCmd = &cobra.Command{Use: "profile", Short: "Back up or restore the RetroArch setup"}

var profileBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Save a portable, hash-verified snapshot of the device (or a local --source folder)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		o := device.BackupOptions{Profile: profileDir(c)}
		o.Source, _ = c.Flags().GetString("source")
		o.DryRun, _ = c.Flags().GetBool("dry-run")
		o.IncludeSaves, _ = c.Flags().GetBool("include-saves")
		return device.Backup(server(), o)
	},
}

var profileRestoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore a saved snapshot, preserving every replaced remote file",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		dry, _ := c.Flags().GetBool("dry-run")
		return device.Restore(server(), profileDir(c), dry)
	},
}

func profileDir(c *cobra.Command) string {
	if p, _ := c.Flags().GetString("profile"); p != "" {
		return p
	}
	return filepath.Join(dataDir(), "retroarch-profile")
}

var downloadCmd = &cobra.Command{Use: "download", Short: "Download explicit Internet Archive files or pinned artwork"}

func destinations() (fetch.Destinations, error) {
	lib, err := library.Load(dataDir())
	if err != nil {
		return fetch.Destinations{}, err
	}
	consoles := map[string]bool{}
	for name := range lib.Profiles {
		consoles[name] = true
	}
	return fetch.Destinations{Consoles: consoles, Root: projectRoot(), ROMs: romsDir()}, nil
}

var downloadListCmd = &cobra.Command{
	Use:   "list ITEM",
	Short: "List files and checksums in an Internet Archive item",
	Args:  cobra.ExactArgs(1),
	RunE:  func(_ *cobra.Command, a []string) error { return fetch.ListItem(a[0]) },
}

var downloadManifestCmd = &cobra.Command{
	Use:   "manifest FILE",
	Short: "Download the explicit item/file/destination entries of a manifest",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, a []string) error {
		raw, err := readFile(a[0])
		if err != nil {
			return err
		}
		plan, err := fetch.ArchivePlan(raw)
		if err != nil {
			return err
		}
		return runDownload(c, plan)
	},
}

var downloadBezelsCmd = &cobra.Command{
	Use:   "bezels",
	Short: "Fetch the six hash-pinned upstream console bezel PNGs",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		plan, err := fetch.BezelPlan(dataDir())
		if err != nil {
			return err
		}
		return runDownload(c, plan)
	},
}

func runDownload(c *cobra.Command, plan []fetch.Plan) error {
	d, err := destinations()
	if err != nil {
		return err
	}
	dry, _ := c.Flags().GetBool("dry-run")
	return fetch.Download(plan, d, dry)
}

var bezelsCmd = &cobra.Command{Use: "bezels", Short: "Install console bezels on the Apple TV"}

var bezelsInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Preview bezel installation; pass --apply to upload missing files",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		apply, _ := c.Flags().GetBool("apply")
		return device.InstallBezels(server(), filepath.Join(dataDir(), "bezels"),
			filepath.Join(dataDir(), "state", "bezels"), apply)
	},
}

var configCmd = &cobra.Command{Use: "config", Short: "Manage the device's main RetroArch config"}

var configPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Replace the device's config/retroarch.cfg with a local file (the old one is backed up)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		file, _ := c.Flags().GetString("file")
		dry, _ := c.Flags().GetBool("dry-run")
		if file == "" {
			file = filepath.Join(projectRoot(), "retroarch.cfg")
		}
		return device.PushConfig(server(), file, filepath.Join(dataDir(), "state", "config-backups"), dry)
	},
}

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Reject tracked runtime data, assets or unexpected files before publishing",
	Args:  cobra.NoArgs,
	RunE:  func(*cobra.Command, []string) error { return repocheck.Check(projectRoot()) },
}

func init() {
	catalogCmd.Flags().Int("count", defaults["count"].(int), "games to export per console")
	viper.BindPFlag("count", catalogCmd.Flags().Lookup("count"))
	viper.BindPFlag("device_home", playlistsCmd.Flags().Lookup("device-home"))
	catalogCmd.Flags().Bool("unzip-export", false, "extract single-ROM archives in the export")
	catalogCmd.Flags().Bool("from-catalog", false, "reuse existing catalog (catalog.sqlite or catalog.csv) without rescanning source folders")

	playlistsCmd.Flags().String("export", "", "export folder (default: <roms>/<export_prefix><count>)")
	playlistsCmd.Flags().String("output", "", "output folder (default: rom_library/state/apple-tv-playlists)")
	playlistsCmd.Flags().String("device-home", "~", "Apple RetroArch home alias (~), or an absolute app-container path")

	syncCmd.Flags().String("export", "", "export folder (default: <roms>/<export_prefix><count>)")
	syncCmd.Flags().Bool("dry-run", false, "preview without uploading")

	profileCmd.PersistentFlags().String("profile", "", "profile folder (default: rom_library/retroarch-profile)")
	profileBackupCmd.Flags().String("source", "", "back up from a local RetroArch data folder instead of the device")
	profileBackupCmd.Flags().Bool("dry-run", false, "preview without writing")
	profileBackupCmd.Flags().Bool("include-saves", false, "also include saves/ and states/")
	profileRestoreCmd.Flags().Bool("dry-run", false, "preview without uploading")
	profileCmd.AddCommand(profileBackupCmd, profileRestoreCmd)

	downloadCmd.PersistentFlags().Bool("dry-run", false, "preview without writing")
	downloadCmd.AddCommand(downloadListCmd, downloadManifestCmd, downloadBezelsCmd)

	bezelsInstallCmd.Flags().Bool("apply", false, "upload missing files (default: preview only)")
	bezelsCmd.AddCommand(bezelsInstallCmd)

	configPushCmd.Flags().String("file", "", "local config to upload (default: <root>/retroarch.cfg)")
	configPushCmd.Flags().Bool("dry-run", false, "preview without uploading")
	configCmd.AddCommand(configPushCmd)
}

func readFile(p string) ([]byte, error) {
	data, err := osReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	return data, nil
}

var osReadFile = os.ReadFile
