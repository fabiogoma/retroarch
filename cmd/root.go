// Package cmd defines the cobra command tree.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/fabiogoma/retroarch/internal/artwork"
	"github.com/fabiogoma/retroarch/internal/device"
	"github.com/fabiogoma/retroarch/internal/library"
)

// Defaults for every setting; config/retroarch-tool.yaml, config/retroarch-tool.local.yaml,
// RETROARCH_* environment variables, and CLI flags override them in that order.
var defaults = map[string]any{
	"server":                "http://192.168.0.249",
	"device_home":           "~",
	"remote_rom_root":       []string{"downloads", "ROMS"},
	"remote_playlists_dir":  "playlists",
	"remote_thumbnails_dir": "thumbnails",
	"remote_overlays_dir":   []string{"overlays", "console-bezels"},
	"remote_config_dir":     "config",
	"roms_dir":              "ROMs",
	"artwork_dir":           "artwork",
	"config_dir":            "config",
	"bezel_dir":             "config/bezels",
	"state_dir":             "state",
	"backup_dir":            "state/config-backups",
	"top":                   "30,40,50",
	"sync_top":              50,
	"export_prefix":         "AppleTV-Top",
	"unzip":                 false,
	"workers":               4,
	"artwork_workers":       8,
	"sync_workers":          4,
	"connect_timeout":       "30s",
	"response_timeout":      "120s",
	"user_agent":            "RetroArchAppleTV/1.0",
	"artwork_api_base":      "https://api.github.com",
	"artwork_raw_base":      "https://raw.githubusercontent.com",
}

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "retroarch",
	Short: "Build curated Top 30/40/50 ROM sets and sync them to RetroArch on Apple TV",
	Long: "Scans local ROMs, builds curated Top 30 / Top 40 / Top 50 export folders with playlists, " +
		"downloads box art and console bezels in parallel, and syncs ROMs, playlists, artwork, bezels, and " +
		"DualShock 4 configuration to RetroArch's web file server in parallel.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the CLI.
func Execute() error { return rootCmd.Execute() }

func init() {
	for k, v := range defaults {
		viper.SetDefault(k, v)
	}
	cobra.OnInitialize(initConfig)
	f := rootCmd.PersistentFlags()
	f.StringVar(&cfgFile, "config", "", "config file (default: config/retroarch-tool.yaml + optional config/retroarch-tool.local.yaml)")
	f.String("root", "", "project root (default: nearest parent containing config/curation.yaml)")
	f.String("roms", "", "ROMs folder (overrides roms_dir in config)")
	f.String("server", "", "RetroArch web server URL (overrides server in config)")
	f.Int("workers", 0, "parallel goroutines (overrides workers / artwork_workers / sync_workers)")
	viper.BindPFlag("root", f.Lookup("root"))
	rootCmd.AddCommand(buildCmd, artworkCmd, syncCmd, checkCmd)
}

func initConfig() {
	viper.SetEnvPrefix("RETROARCH")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
		if err := viper.ReadInConfig(); err != nil {
			fmt.Fprintln(os.Stderr, "Error: reading config:", err)
			os.Exit(1)
		}
	} else {
		cfgDir := filepath.Join(projectRoot(), "config")
		viper.SetConfigName("retroarch-tool")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(cfgDir)
		_ = viper.ReadInConfig()
		localCfg := filepath.Join(cfgDir, "retroarch-tool.local.yaml")
		if info, err := os.Stat(localCfg); err == nil && !info.IsDir() {
			viper.SetConfigFile(localCfg)
			if err := viper.MergeInConfig(); err != nil {
				fmt.Fprintln(os.Stderr, "Error: reading local config:", err)
				os.Exit(1)
			}
		}
	}
	applySettings()
}

func applySettings() {
	connect, err := time.ParseDuration(viper.GetString("connect_timeout"))
	response, err2 := time.ParseDuration(viper.GetString("response_timeout"))
	if err != nil || err2 != nil {
		fmt.Fprintln(os.Stderr, "Error: connect_timeout and response_timeout must be durations such as 30s")
		os.Exit(1)
	}
	device.ConnectTimeout, device.ResponseTimeout = connect, response
	device.RemoteROMRoot = viper.GetStringSlice("remote_rom_root")
	device.RemotePlaylistsDir = viper.GetString("remote_playlists_dir")
	device.RemoteThumbnailsDir = viper.GetString("remote_thumbnails_dir")
	device.RemoteOverlaysDir = viper.GetStringSlice("remote_overlays_dir")
	device.RemoteConfigDir = viper.GetString("remote_config_dir")

	artwork.ResponseTimeout = response
	artwork.UserAgent = viper.GetString("user_agent")
	artwork.GitHubAPIBase = viper.GetString("artwork_api_base")
	artwork.GitHubRawBase = viper.GetString("artwork_raw_base")

	library.ExportPrefix = viper.GetString("export_prefix")
}

func projectRoot() string {
	if r := viper.GetString("root"); r != "" {
		abs, _ := filepath.Abs(r)
		return abs
	}
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "config", "curation.yaml")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}

func resolvePath(cfgKey string) string {
	p := viper.GetString(cfgKey)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectRoot(), p)
}

func configDir() string  { return resolvePath("config_dir") }
func artworkDir() string { return resolvePath("artwork_dir") }
func bezelDir() string   { return resolvePath("bezel_dir") }
func stateDir() string   { return resolvePath("state_dir") }
func backupDir() string  { return resolvePath("backup_dir") }

func romsDir(c *cobra.Command) string {
	if c != nil && c.Flags().Changed("roms") {
		if r, _ := c.Flags().GetString("roms"); r != "" {
			abs, _ := filepath.Abs(r)
			return abs
		}
	}
	return resolvePath("roms_dir")
}

func serverURL(c *cobra.Command) string {
	if c != nil && c.Flags().Changed("server") {
		if s, _ := c.Flags().GetString("server"); s != "" {
			return s
		}
	}
	return viper.GetString("server")
}

func workersFor(c *cobra.Command, key string) int {
	if c != nil && c.Flags().Changed("workers") {
		if w, _ := c.Flags().GetInt("workers"); w > 0 {
			return w
		}
	}
	if w := viper.GetInt(key); w > 0 {
		return w
	}
	if w := viper.GetInt("workers"); w > 0 {
		return w
	}
	return 4
}

func exportDirFor(c *cobra.Command, top int) string {
	return filepath.Join(romsDir(c), fmt.Sprintf("%s%d", viper.GetString("export_prefix"), top))
}
