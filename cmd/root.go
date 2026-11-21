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

// Defaults for every setting; retroarch-tool.yaml, RETROARCH_* variables and flags override them.
var defaults = map[string]any{
	"server":           "http://192.168.0.249",
	"top":              "30,40,50",
	"sync_top":         50,
	"export_prefix":    "AppleTV-Top",
	"device_home":      "~",
	"remote_rom_root":  []string{"downloads", "ROMS"},
	"workers":          4,
	"connect_timeout":  "30s",
	"response_timeout": "120s",
	"user_agent":       "RetroArchAppleTV/1.0",
}

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "retroarch",
	Short: "Build curated Top 30/40/50 ROM sets and sync them to RetroArch on Apple TV",
	Long: "Scans local ROMs, builds curated Top 30 / Top 40 / Top 50 export folders with playlists, " +
		"downloads box art and console bezels, and syncs ROMs, playlists, artwork, bezels, and " +
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
	f.StringVar(&cfgFile, "config", "", "config file (default: retroarch-tool.yaml in the project root)")
	f.String("root", "", "project root (default: nearest parent containing rom_library/)")
	f.String("roms", "", "ROMs folder (default: <root>/ROMs)")
	f.String("server", defaults["server"].(string), "RetroArch web server URL on the Apple TV")
	f.Int("workers", defaults["workers"].(int), "parallel goroutines for artwork and sync transfers")
	viper.BindPFlag("root", f.Lookup("root"))
	viper.BindPFlag("roms", f.Lookup("roms"))
	viper.BindPFlag("server", f.Lookup("server"))
	viper.BindPFlag("workers", f.Lookup("workers"))
	rootCmd.AddCommand(buildCmd, artworkCmd, syncCmd, checkCmd)
}

func initConfig() {
	viper.SetEnvPrefix("RETROARCH")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("retroarch-tool")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(projectRoot())
	}
	if err := viper.ReadInConfig(); err != nil && cfgFile != "" {
		fmt.Fprintln(os.Stderr, "Error: reading config:", err)
		os.Exit(1)
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
	artwork.ResponseTimeout = response
	artwork.UserAgent = viper.GetString("user_agent")
	device.RemoteROMRoot = viper.GetStringSlice("remote_rom_root")
	library.ExportPrefix = viper.GetString("export_prefix")
}

func projectRoot() string {
	if r := viper.GetString("root"); r != "" {
		abs, _ := filepath.Abs(r)
		return abs
	}
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if info, err := os.Stat(filepath.Join(d, "rom_library")); err == nil && info.IsDir() {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}

func dataDir() string    { return filepath.Join(projectRoot(), "rom_library") }
func configDir() string  { return filepath.Join(projectRoot(), "config") }
func artworkDir() string { return filepath.Join(projectRoot(), "artwork") }

func romsDir() string {
	if r := viper.GetString("roms"); r != "" {
		abs, _ := filepath.Abs(r)
		return abs
	}
	return filepath.Join(projectRoot(), "ROMs")
}

func exportDirFor(top int) string {
	return filepath.Join(romsDir(), fmt.Sprintf("%s%d", viper.GetString("export_prefix"), top))
}
