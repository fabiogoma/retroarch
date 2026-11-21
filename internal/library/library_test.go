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
	t     *testing.T
	roms  string
	state string
	lib   *Library
	db    DB
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, roms: filepath.Join(root, "ROMs"), state: filepath.Join(root, "state")}
	for _, d := range []string{filepath.Join(f.roms, "nes"), f.state} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.lib = &Library{
		Profiles: map[string]Profile{"nes": {Extensions: []string{".nes"}, FolderAliases: []string{"nes"}}},
		romExts:  map[string]bool{".nes": true},
		Curated: map[string][]Entry{"nes": {{Title: "Family Game", Aliases: []string{}, PlayMode: "2 players",
			FamilyNotes: "Cartoon combat", Reason: "Family play", Popularity: "Test fixture", Sources: []any{}}}},
	}
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f.db = db
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

func (f *fixture) run(count int, unzip bool) {
	f.t.Helper()
	if err := f.lib.Scan(f.roms, f.state, f.db); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.lib.Export(f.roms, f.state, f.db, count, unzip); err != nil {
		f.t.Fatal(err)
	}
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

func (f *fixture) count(query string) int {
	var n int
	if err := f.db.QueryRow(query).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestSingleROMZipStaysCompressed(t *testing.T) {
	f := newFixture(t)
	path := f.zip("Family Game (USA).zip", map[string][]byte{"Family Game (USA).nes": append([]byte("NES\x1a"), bytes.Repeat([]byte("abc"), 100)...)})
	before, _ := os.ReadFile(path)
	f.run(30, false)
	var member string
	f.db.QueryRow("select member from games").Scan(&member)
	if member != "Family Game (USA).nes" {
		t.Fatalf("member = %q", member)
	}
	sel := f.selection(30)
	got, _ := os.ReadFile(filepath.Join(f.roms, "AppleTV-Top30", "nes", sel[0].File))
	if !bytes.Equal(got, before) {
		t.Fatal("export differs from the original archive")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
		t.Fatal("original archive changed")
	}
}

func TestNestedBundleAndIncompleteFiles(t *testing.T) {
	f := newFixture(t)
	var inner bytes.Buffer
	w := zip.NewWriter(&inner)
	fw, _ := w.Create("Family Game (USA).nes")
	fw.Write([]byte("NES\x1aabc"))
	w.Close()
	path := f.zip("bundle.zip", map[string][]byte{"one.zip": inner.Bytes(), "Second Game (USA).nes": []byte("NES\x1adef")})
	os.WriteFile(filepath.Join(f.roms, "nes", "Unfinished.nes.part"), []byte("partial"), 0o644)
	if err := f.lib.Scan(f.roms, f.state, f.db); err != nil {
		t.Fatal(err)
	}
	if n := f.count("select count(*) from games"); n != 2 {
		t.Fatalf("games = %d", n)
	}
	if n := f.count("select count(*) from files where status='incomplete'"); n != 1 {
		t.Fatalf("incomplete = %d", n)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("original bundle removed")
	}
}

func TestTraversalIsRejectedWithoutWriting(t *testing.T) {
	f := newFixture(t)
	f.zip("bad.zip", map[string][]byte{"../../outside.nes": []byte("bad"), "normal.nes": []byte("good")})
	if err := f.lib.Scan(f.roms, f.state, f.db); err != nil {
		t.Fatal(err)
	}
	if n := f.count("select count(*) from games"); n != 0 {
		t.Fatalf("games = %d", n)
	}
	var msg string
	f.db.QueryRow("select message from issues").Scan(&msg)
	if !strings.Contains(msg, "Unsafe member") {
		t.Fatalf("message = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.roms), "outside.nes")); err == nil {
		t.Fatal("traversal wrote outside the extraction directory")
	}
}

func TestRegionPreferenceAndStaleGameRemoval(t *testing.T) {
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
	f.run(30, false)
	sel = f.selection(30)
	if sel[0].Source != europe || f.count("select count(*) from games") != 1 {
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
	if sum, _ := hashFileOnly(out); sum != sel[0].ExportSHA256 {
		t.Fatal("export hash does not match manifest")
	}

	original := copyFile
	copyFile = func(string, string) error { return errors.New("disk full") }
	defer func() { copyFile = original }()
	if err := f.lib.Scan(f.roms, f.state, f.db); err != nil {
		t.Fatal(err)
	}
	if _, err := f.lib.Export(f.roms, f.state, f.db, 30, false); err == nil {
		t.Fatal("expected export failure")
	}
	if got, _ := os.ReadFile(out); string(got) != "NES\x1a123" {
		t.Fatalf("previous export changed: %q", got)
	}
}

func TestNormalizedAndRegionScore(t *testing.T) {
	if got := Normalized("The Mickey's Adventure & Friends (USA)"); got != "mickeys adventure and friends" {
		t.Fatalf("normalized = %q", got)
	}
	if RegionScore("Game (USA)") <= RegionScore("Game (Europe)") || RegionScore("Game (Japan)") >= 0 || RegionScore("Game (USA) (Beta)") > -70 {
		t.Fatal("unexpected region scores")
	}
}

func TestRunCatalogFromCSVRemapsMovedPaths(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "rom_library")
	stateDir := filepath.Join(dataDir, "state")
	romsDir := filepath.Join(root, "ROMs")
	if err := os.MkdirAll(filepath.Join(romsDir, "nes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	romPath := filepath.Join(romsDir, "nes", "Super Mario Bros. (USA).nes")
	if err := os.WriteFile(romPath, []byte("NES-ROM-DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := hashFileOnly(romPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "consoles.json"), []byte(`{"nes":{"extensions":[".nes"],"folder_aliases":["nes"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "curation.json"), []byte(`{"nes":[{"title":"Super Mario Bros.","aliases":[],"play_mode":"2P","genre":"Platformer","reason":"Classic","family_notes":"Friendly","popularity":"Best seller","sources":[]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a moved workspace path in catalog.csv while catalog.sqlite has no rows yet.
	csvContent := "id,console,title,title_key,path,member,sha256,bytes,region_score\n" +
		"1,nes,Super Mario Bros.,super mario bros,/old/workspace/ROMs/nes/Super Mario Bros. (USA).nes,," + sha + ",12,30\n"
	if err := os.WriteFile(filepath.Join(stateDir, "catalog.csv"), []byte(csvContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RunCatalog(dataDir, romsDir, 1, false, true); err != nil {
		t.Fatalf("RunCatalog(--from-catalog) failed: %v", err)
	}
	exported := filepath.Join(romsDir, "AppleTV-Top1", "nes", "Super Mario Bros..nes")
	if _, err := os.Stat(exported); err != nil {
		t.Fatalf("expected exported ROM at %s: %v", exported, err)
	}
}

func hashFileOnly(path string) (string, error) {
	sum, _, err := hashFile(path)
	return sum, err
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
