package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratePlaylists(t *testing.T) {
	export, data := t.TempDir(), t.TempDir()
	for _, p := range PlaylistNames {
		os.MkdirAll(filepath.Join(export, p.Console), 0o755)
		sel := `[{"title":"Game","file":"Game.zip","archive_member":"Game.nes"}]`
		os.WriteFile(filepath.Join(export, p.Console, "selection.json"), []byte(sel), 0o644)
	}
	os.WriteFile(filepath.Join(data, "apple_tv_cores.json"), []byte(`[{"system":"Nintendo - Nintendo Entertainment System","core_path":":/Frameworks/nestopia.libretro.framework","core_name":"Nestopia"}]`), 0o644)
	out := filepath.Join(t.TempDir(), "out")
	for _, bad := range []string{"relative", "/a/../b"} {
		if err := GeneratePlaylists(data, export, out, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := GeneratePlaylists(data, export, out, "~"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "Nintendo - Nintendo Entertainment System.lpl"))
	var pl playlist
	if err := json.Unmarshal(raw, &pl); err != nil {
		t.Fatal(err)
	}
	it := pl.Items[0]
	if it.Path != "~/Library/Caches/RetroArch/downloads/ROMS/nes/Game.zip#Game.nes" || it.CorePath != ":/Frameworks/nestopia.libretro.framework" ||
		it.DBName != "Nintendo - Nintendo Entertainment System.lpl" || pl.Version != "1.5" {
		t.Fatalf("item = %+v", it)
	}
	other, _ := os.ReadFile(filepath.Join(out, "Nintendo - Nintendo 64.lpl"))
	if !strings.Contains(string(other), `"core_path": "DETECT"`) {
		t.Fatal("missing core should default to DETECT")
	}
}
