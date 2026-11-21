// Package cmd defines the cobra command tree; every task is a subcommand.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const defaultServer = "http://192.168.0.249"

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
	cobra.OnInitialize(initConfig)
	f := rootCmd.PersistentFlags()
	f.StringVar(&cfgFile, "config", "", "config file (default: retroarch-tool.yaml in the project root)")
	f.String("root", "", "project root (default: nearest parent containing rom_library/)")
	f.String("roms", "", "ROMs folder (default: <root>/ROMs)")
	f.String("server", defaultServer, "RetroArch web server URL on the Apple TV")
	viper.BindPFlag("root", f.Lookup("root"))
	viper.BindPFlag("roms", f.Lookup("roms"))
	viper.BindPFlag("server", f.Lookup("server"))
	rootCmd.AddCommand(catalogCmd, playlistsCmd, syncCmd, profileCmd, downloadCmd, bezelsCmd, checkCmd)
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

func defaultExport() string { return filepath.Join(romsDir(), "AppleTV-Top40") }
