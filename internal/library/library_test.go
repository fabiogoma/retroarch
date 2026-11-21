package library

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	t       *testing.T
	dataDir string
	roms    string
	state   string
	lib     *Library
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:       t,
		dataDir: filepath.Join(root, "rom_library"),
		roms:    filepath.Join(root, "ROMs"),
		state:   filepath.Join(root, "rom_library", "state"),
	}
	for _, d := range []string{filepath.Join(f.roms, "nes"), f.state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cores := `[{"system":"Nintendo - Nintendo Entertainment System","core_path":":/Frameworks/nestopia.libretro.framework","core_name":"Nintendo - NES / Famicom (Nestopia)"}]`
	if err := os.WriteFile(filepath.Join(f.dataDir, "apple_tv_cores.json"), []byte(cores), 0o644); err != nil {
		t.Fatal(err)
	}
	f.lib = &Library{
		Profiles: map[string]Profile{"nes": {Extensions: []string{".nes"}, FolderAliases: []string{"nes"}}},
		romExts:  map[string]bool{".nes": true},
		Curated: map[string][]Entry{"nes": {{Title: "Family Game", Aliases: []string{}, PlayMode: "2 players",
			FamilyNotes: "Cartoon combat", Reason: "Family play", Popularity: "Test fixture", Sources: []any{}}}},
	}
	return f
}

func (f *fixture) zip(name string, contents map[string][]byte) string {
	f.t.Helper()
	path := filepath.Join(f.roms, "nes", name)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for n, b := range contents {
		fw, _ := w.Create(n)
		fw.Write(b)
	}
	w.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) run(count int, unzip bool) *ScanResult {
	f.t.Helper()
	scan, err := f.lib.Scan(f.roms, f.state)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.lib.Export(f.dataDir, f.roms, f.state, scan, count, unzip, "~"); err != nil {
		f.t.Fatal(err)
	}
	return scan
}

func (f *fixture) selection(count int) []Selection {
	f.t.Helper()
	var out []Selection
	data, err := os.ReadFile(filepath.Join(f.roms, "AppleTV-Top30", "nes", "selection.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestSingleROMZipStaysCompressedAndWritesPlaylist(t *testing.T) {
	f := newFixture(t)
	path := f.zip("Family Game (USA).zip", map[string][]byte{"Family Game (USA).nes": append([]byte("NES\x1a"), bytes.Repeat([]byte("abc"), 100)...)})
	before, _ := os.ReadFile(path)
	scan := f.run(30, false)
	if len(scan.Games) != 1 || scan.Games[0].Member != "Family Game (USA).nes" {
		t.Fatalf("games = %+v", scan.Games)
	}
	sel := f.selection(30)
	got, _ := os.ReadFile(filepath.Join(f.roms, "AppleTV-Top30", "nes", sel[0].File))
	if !bytes.Equal(got, before) {
		t.Fatal("export differs from the original archive")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
		t.Fatal("original archive changed")
	}
	plRaw, err := os.ReadFile(filepath.Join(f.roms, "AppleTV-Top30", "playlists", "Nintendo - Nintendo Entertainment System.lpl"))
	if err != nil {
		t.Fatalf("missing playlist: %v", err)
	}
	if !strings.Contains(string(plRaw), "~/Library/Caches/RetroArch/downloads/ROMS/nes/Family Game.zip#Family Game (USA).nes") ||
		!strings.Contains(string(plRaw), ":/Frameworks/nestopia.libretro.framework") {
		t.Fatalf("unexpected playlist content: %s", plRaw)
	}
}

func TestNestedBundle(t *testing.T) {
	f := newFixture(t)
	var inner bytes.Buffer
	w := zip.NewWriter(&inner)
	fw, _ := w.Create("Family Game (USA).nes")
	fw.Write([]byte("NES\x1aabc"))
	w.Close()
	path := f.zip("bundle.zip", map[string][]byte{"one.zip": inner.Bytes(), "Second Game (USA).nes": []byte("NES\x1adef")})
	scan, err := f.lib.Scan(f.roms, f.state)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Games) != 2 {
		t.Fatalf("games = %d, want 2", len(scan.Games))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("original bundle removed")
	}
}

func TestTraversalIsRejectedWithoutWriting(t *testing.T) {
	f := newFixture(t)
	f.zip("bad.zip", map[string][]byte{"../../outside.nes": []byte("bad"), "normal.nes": []byte("good")})
	scan, err := f.lib.Scan(f.roms, f.state)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Games) != 0 {
		t.Fatalf("games = %d, want 0", len(scan.Games))
	}
	if len(scan.Issues) == 0 || !strings.Contains(scan.Issues[0].Message, "Unsafe member") {
		t.Fatalf("issues = %+v", scan.Issues)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.roms), "outside.nes")); err == nil {
		t.Fatal("traversal wrote outside the extraction directory")
	}
}

func TestRegionPreferenceAndRemoval(t *testing.T) {
	f := newFixture(t)
	usa := filepath.Join(f.roms, "nes", "Family Game (USA).nes")
	europe := filepath.Join(f.roms, "nes", "Family Game (Europe).nes")
	os.WriteFile(usa, []byte("usa"), 0o644)
	os.WriteFile(europe, []byte("eu"), 0o644)
	f.run(30, false)
	sel := f.selection(30)
	if len(sel) != 1 || sel[0].Source != usa {
		t.Fatalf("selection = %+v", sel)
	}
	os.Remove(usa)
	scan := f.run(30, false)
	sel = f.selection(30)
	if sel[0].Source != europe || len(scan.Games) != 1 {
		t.Fatalf("selection after removal = %+v", sel)
	}
}

func TestUnzipExportAndFailedExportKeepsPreviousSet(t *testing.T) {
	f := newFixture(t)
	f.zip("Family Game.zip", map[string][]byte{"Family Game.nes": []byte("NES\x1a123")})
	f.run(30, true)
	sel := f.selection(30)
	if !strings.HasSuffix(sel[0].File, ".nes") {
		t.Fatalf("file = %q", sel[0].File)
	}
	out := filepath.Join(f.roms, "AppleTV-Top30", "nes", sel[0].File)
	if sum, _, _ := hashFile(out); sum != sel[0].ExportSHA256 {
		t.Fatal("export hash does not match manifest")
	}

	original := copyFile
	copyFile = func(string, string) error { return errors.New("disk full") }
	defer func() { copyFile = original }()
	scan, err := f.lib.Scan(f.roms, f.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.lib.Export(f.dataDir, f.roms, f.state, scan, 30, false, "~"); err == nil {
		t.Fatal("expected export failure")
	}
	if got, _ := os.ReadFile(out); string(got) != "NES\x1a123" {
		t.Fatalf("previous export changed: %q", got)
	}
}

func TestBuildMultipleTopSetsAndCacheReuse(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "rom_library")
	romsDir := filepath.Join(root, "ROMs")
	if err := os.MkdirAll(filepath.Join(romsDir, "nes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dataDir, "consoles.json"), []byte(`{"nes":{"extensions":[".nes"],"folder_aliases":["nes"]}}`), 0o644)
	os.WriteFile(filepath.Join(dataDir, "apple_tv_cores.json"), []byte(`[{"system":"Nintendo - Nintendo Entertainment System","core_path":":/Frameworks/nestopia.libretro.framework","core_name":"Nestopia"}]`), 0o644)
	os.WriteFile(filepath.Join(dataDir, "curation.json"), []byte(`{"nes":[{"title":"Game One","aliases":[],"play_mode":"1P","genre":"Action","reason":"A","family_notes":"","popularity":"","sources":[]},{"title":"Game Two","aliases":[],"play_mode":"2P","genre":"Action","reason":"B","family_notes":"","popularity":"","sources":[]}]}`), 0o644)
	os.WriteFile(filepath.Join(romsDir, "nes", "Game One (USA).nes"), []byte("rom1"), 0o644)
	os.WriteFile(filepath.Join(romsDir, "nes", "Game Two (USA).nes"), []byte("rom2"), 0o644)

	if err := Build(BuildOptions{DataDir: dataDir, ROMsDir: romsDir, Counts: []int{1, 2}}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	sel1, err := LoadExportSelections(filepath.Join(romsDir, "AppleTV-Top1"))
	if err != nil || len(sel1["nes"]) != 1 {
		t.Fatalf("Top1 = %v, err=%v", sel1, err)
	}
	sel2, err := LoadExportSelections(filepath.Join(romsDir, "AppleTV-Top2"))
	if err != nil || len(sel2["nes"]) != 2 {
		t.Fatalf("Top2 = %v, err=%v", sel2, err)
	}
	// Rebuild using --from-cache.
	if err := Build(BuildOptions{DataDir: dataDir, ROMsDir: romsDir, Counts: []int{2}, FromCache: true}); err != nil {
		t.Fatalf("Build(--from-cache) failed: %v", err)
	}
}

func TestNormalizedRegionScoreAndParseCounts(t *testing.T) {
	if got := Normalized("The Mickey's Adventure & Friends (USA)"); got != "mickeys adventure and friends" {
		t.Fatalf("normalized = %q", got)
	}
	if RegionScore("Game (USA)") <= RegionScore("Game (Europe)") || RegionScore("Game (Japan)") >= 0 || RegionScore("Game (USA) (Beta)") > -70 {
		t.Fatal("unexpected region scores")
	}
	if got := SanitizeThumbnailName("Chip 'n Dale: Rescue Rangers & Friends"); got != "Chip 'n Dale_ Rescue Rangers _ Friends" {
		t.Fatalf("sanitized = %q", got)
	}
	counts, err := ParseCounts("30,40,50")
	if err != nil || len(counts) != 3 || counts[0] != 30 || counts[1] != 40 || counts[2] != 50 {
		t.Fatalf("ParseCounts = %v, %v", counts, err)
	}
}

// Set RETROARCH_TEST_RAR to a real RAR (e.g. the PC Engine collection) to check RAR support.
func TestRealRAR(t *testing.T) {
	p := os.Getenv("RETROARCH_TEST_RAR")
	if p == "" {
		t.Skip("RETROARCH_TEST_RAR not set")
	}
	members, err := ListArchive(p)
	if err != nil || len(members) < 400 {
		t.Fatalf("members=%d err=%v", len(members), err)
	}
}
