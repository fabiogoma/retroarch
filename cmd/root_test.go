package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestInitConfigLoadsFromConfigDirAndMergesLocal(t *testing.T) {
	viper.Reset()
	t.Cleanup(func() {
		viper.Reset()
		for k, v := range defaults {
			viper.SetDefault(k, v)
		}
		cfgFile = ""
	})
	for k, v := range defaults {
		viper.SetDefault(k, v)
	}

	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "retroarch-tool.yaml"), []byte(
		"server: http://10.0.0.10\nworkers: 6\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "retroarch-tool.local.yaml"), []byte(
		"server: http://10.0.0.99\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray root-level config should not be picked up.
	if err := os.WriteFile(filepath.Join(root, "retroarch-tool.yaml"), []byte(
		"server: http://10.0.0.250\nworkers: 1\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgFile = ""
	viper.Set("root", root)
	initConfig()

	if got := viper.GetString("server"); got != "http://10.0.0.99" {
		t.Fatalf("server = %q, want http://10.0.0.99 from config/retroarch-tool.local.yaml", got)
	}
	if got := viper.GetInt("workers"); got != 6 {
		t.Fatalf("workers = %d, want 6 from config/retroarch-tool.yaml", got)
	}
}
