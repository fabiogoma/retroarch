// Package cmd defines the cobra command tree; every task is a subcommand.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/fabiogoma/retroarch/internal/device"
	"github.com/fabiogoma/retroarch/internal/fetch"
	"github.com/fabiogoma/retroarch/internal/library"
)

// Defaults for every setting; retroarch-tool.yaml, RETROARCH_* variables and flags override them.
var defaults = map[string]any{
	"server":             "http://192.168.0.249",
	"count":              40,
	"export_prefix":      "AppleTV-Top",
	"device_home":        "~",
	"remote_rom_root":    []string{"downloads", "ROMS"},
	"connect_timeout":    "30s",
	"response_timeout":   "120s",
	"max_download_bytes": int64(32 << 30),
	"user_agent":         "RetroArchAppleTV/1.0",
}

var cfgFile string

var rootCmd = &cobra.Command{
	Use:           "retroarch",
	Short:         "ROM library and Apple TV RetroArch setup tool",
	Long:          "Catalogs the ROM library, builds the Apple TV export and playlists, and syncs ROMs, settings and bezels to RetroArch's web file server.\n\nRun from the project root (the folder containing rom_library/), or pass --root.\nSettings may also come from RETROARCH_* environment variables or a retroarch-tool.yaml file.",
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
	viper.BindPFlag("root", f.Lookup("root"))
	viper.BindPFlag("roms", f.Lookup("roms"))
	viper.BindPFlag("server", f.Lookup("server"))
	rootCmd.AddCommand(catalogCmd, playlistsCmd, syncCmd, profileCmd, downloadCmd, bezelsCmd, configCmd, checkCmd)
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
	fetch.ResponseTimeout = response
	fetch.MaxBytes = viper.GetInt64("max_download_bytes")
	fetch.UserAgent = viper.GetString("user_agent")
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

func dataDir() string { return filepath.Join(projectRoot(), "rom_library") }

func romsDir() string {
	if r := viper.GetString("roms"); r != "" {
		abs, _ := filepath.Abs(r)
		return abs
	}
	return filepath.Join(projectRoot(), "ROMs")
}

func defaultExport() string {
	return filepath.Join(romsDir(), fmt.Sprintf("%s%d", viper.GetString("export_prefix"), viper.GetInt("count")))
}
