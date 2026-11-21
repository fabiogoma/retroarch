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
		dataDir: filepath.Join(root, "config"),
		roms:    filepath.Join(root, "ROMs"),
		state:   filepath.Join(root, "state"),
	}
	for _, d := range []string{filepath.Join(f.roms, "nes"), f.dataDir, f.state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cores := "- system: Nintendo - Nintendo Entertainment System\n  core_path: :/Frameworks/nestopia.libretro.framework\n  core_name: Nintendo - NES / Famicom (Nestopia)\n"
	if err := os.WriteFile(filepath.Join(f.dataDir, "apple_tv_cores.yaml"), []byte(cores), 0o644); err != nil {
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
	scan, err := f.lib.Scan(f.roms)
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

func TestMultiFileBundleZipReportedAsIssue(t *testing.T) {
	f := newFixture(t)
	f.zip("bundle.zip", map[string][]byte{"One (USA).nes": []byte("NES\x1aabc"), "Two (USA).nes": []byte("NES\x1adef")})
	scan, err := f.lib.Scan(f.roms)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Games) != 0 {
		t.Fatalf("games = %d, want 0", len(scan.Games))
	}
	if len(scan.Issues) != 1 || !strings.Contains(scan.Issues[0].Message, "Multi-file ZIP bundle") {
		t.Fatalf("issues = %+v", scan.Issues)
	}
}

func TestTraversalIsRejectedWithoutWriting(t *testing.T) {
	f := newFixture(t)
	f.zip("bad.zip", map[string][]byte{"../../outside.nes": []byte("bad"), "normal.nes": []byte("good")})
	scan, err := f.lib.Scan(f.roms)
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
	scan, err := f.lib.Scan(f.roms)
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
	dataDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")
	romsDir := filepath.Join(root, "ROMs")
	if err := os.MkdirAll(filepath.Join(romsDir, "nes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dataDir, "consoles.yaml"), []byte("nes:\n  extensions: [.nes]\n  folder_aliases: [nes]\n"), 0o644)
	os.WriteFile(filepath.Join(dataDir, "apple_tv_cores.yaml"), []byte("- system: Nintendo - Nintendo Entertainment System\n  core_path: :/Frameworks/nestopia.libretro.framework\n  core_name: Nestopia\n"), 0o644)
	os.WriteFile(filepath.Join(dataDir, "curation.yaml"), []byte("nes:\n  - title: Game One\n    aliases: []\n    play_mode: 1P\n    genre: Action\n    reason: A\n    family_notes: \"\"\n    popularity: \"\"\n    sources: []\n  - title: Game Two\n    aliases: []\n    play_mode: 2P\n    genre: Action\n    reason: B\n    family_notes: \"\"\n    popularity: \"\"\n    sources: []\n"), 0o644)
	os.WriteFile(filepath.Join(romsDir, "nes", "Game One (USA).nes"), []byte("rom1"), 0o644)
	os.WriteFile(filepath.Join(romsDir, "nes", "Game Two (USA).nes"), []byte("rom2"), 0o644)

	if err := Build(BuildOptions{DataDir: dataDir, StateDir: stateDir, ROMsDir: romsDir, Counts: []int{1, 2}}); err != nil {
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
	if err := Build(BuildOptions{DataDir: dataDir, StateDir: stateDir, ROMsDir: romsDir, Counts: []int{2}, FromCache: true}); err != nil {
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
	counts, err := ParseCounts("10,20,30,40,50,60,70,80,90,100")
	if err != nil || len(counts) != 10 || counts[0] != 10 || counts[9] != 100 {
		t.Fatalf("ParseCounts = %v, %v", counts, err)
	}
	allCounts, err := ParseCounts("all")
	wantAll := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if err != nil || len(allCounts) != len(wantAll) {
		t.Fatalf("ParseCounts(all) = %v, %v", allCounts, err)
	}
	for i, want := range wantAll {
		if allCounts[i] != want {
			t.Fatalf("ParseCounts(all)[%d] = %d, want %d", i, allCounts[i], want)
		}
	}
	emptyCounts, err := ParseCounts("")
	if err != nil || len(emptyCounts) != len(wantAll) {
		t.Fatalf("ParseCounts(\"\") = %v, %v", emptyCounts, err)
	}
}

func TestCurationCoversUpToTop100(t *testing.T) {
	dataDir := filepath.Join("..", "..", "config")
	lib, err := Load(dataDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := lib.LoadCuration(dataDir); err != nil {
		t.Fatalf("LoadCuration: %v", err)
	}
	for _, c := range Consoles {
		entries := lib.Curated[c.Folder]
		if len(entries) < 100 {
			t.Errorf("%s has %d curated entries, want at least 100", c.Folder, len(entries))
		}
		seen := map[string]bool{}
		for _, e := range entries {
			key := Normalized(e.Title)
			if seen[key] {
				t.Errorf("%s has duplicate curated title %q", c.Folder, e.Title)
			}
			seen[key] = true
		}
	}
}
