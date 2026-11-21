package device

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/util"
)

// memory mimics the RetroArch file server, including numbering of duplicate uploads.
type memory struct {
	mu     sync.Mutex
	files  map[string][]byte
	writes []string
}

func newMemory(files map[string]string) *memory {
	m := &memory{files: map[string][]byte{}}
	for k, v := range files {
		m.files[k] = []byte(v)
	}
	return m
}

func (m *memory) URL() string { return "memory://" }

func (m *memory) dirsLocked() map[string]bool {
	d := map[string]bool{"/": true}
	for p := range m.files {
		for dir := path.Dir(p); dir != "/"; dir = path.Dir(dir) {
			d[dir+"/"] = true
		}
	}
	return d
}

func (m *memory) List(folder string) (map[string]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Entry{}
	if !m.dirsLocked()[folder] {
		return nil, fmt.Errorf("HTTP 404")
	}
	for p, data := range m.files {
		if strings.HasPrefix(p, folder) {
			rest := p[len(folder):]
			if rest == ".dir" {
				continue
			}
			if i := strings.Index(rest, "/"); i >= 0 {
				out[rest[:i]] = Entry{Name: rest[:i], Path: folder + rest[:i] + "/"}
			} else {
				size := int64(len(data))
				out[rest] = Entry{Name: rest, Path: p, Size: &size}
			}
		}
	}
	return out, nil
}

func (m *memory) Download(remote string, w io.Writer) (int64, string, error) {
	m.mu.Lock()
	data, ok := m.files[remote]
	if ok {
		data = append([]byte(nil), data...)
	}
	m.mu.Unlock()
	if !ok {
		return 0, "", errors.New("HTTP 404")
	}
	w.Write(data)
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

func (m *memory) Move(oldPath, newPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, "move")
	m.files[newPath] = m.files[oldPath]
	delete(m.files, oldPath)
	return nil
}

func (m *memory) EnsureFolder(parts []string, dryRun bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	folder := "/"
	for _, name := range parts {
		child := folder + name + "/"
		if _, isFile := m.files[folder+name]; isFile {
			return "", fmt.Errorf("Remote path is not a directory: %s", child)
		}
		if !m.dirsLocked()[child] {
			if dryRun {
				return "", nil
			}
			m.writes = append(m.writes, "create")
			m.files[child+".dir"] = nil
		}
		folder = child
	}
	return folder, nil
}

func (m *memory) Upload(folder, localPath string, size int64) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, "upload")
	name := filepath.Base(localPath)
	target := folder + name
	for i := 1; ; i++ {
		if _, ok := m.files[target]; !ok {
			break
		}
		target = fmt.Sprintf("%s%s-%d%s", folder, strings.TrimSuffix(name, path.Ext(name)), i, path.Ext(name))
	}
	m.files[target] = data
	return nil
}

func (m *memory) visible() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for p, d := range m.files {
		if !strings.HasSuffix(p, "/.dir") {
			out[p] = string(d)
		}
	}
	return out
}

func writeExport(t *testing.T, files map[string]string) string {
	t.Helper()
	export := t.TempDir()
	folder := filepath.Join(export, "nes")
	os.MkdirAll(folder, 0o755)
	os.MkdirAll(filepath.Join(export, "playlists"), 0o755)
	var sel []string
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		os.WriteFile(filepath.Join(folder, n), []byte(files[n]), 0o644)
		title := strings.TrimSuffix(n, filepath.Ext(n))
		sel = append(sel, fmt.Sprintf(`{"title":%q,"file":%q,"bytes":%d,"export_sha256":%q}`, title, n, len(files[n]), util.SHA256Hex([]byte(files[n]))))
	}
	os.WriteFile(filepath.Join(folder, "selection.json"), []byte("["+strings.Join(sel, ",")+"]"), 0o644)
	os.WriteFile(filepath.Join(export, "summary.json"), []byte(fmt.Sprintf(`{"consoles":[{"console":"nes","selected":%d}]}`, len(files))), 0o644)
	os.WriteFile(filepath.Join(export, "playlists", "Nintendo - Nintendo Entertainment System.lpl"), []byte(`{"version":"1.5","items":[]}`), 0o644)
	return export
}

func TestSyncAllParallelROMsPlaylistsArtworkBezelsAndConfig(t *testing.T) {
	export := writeExport(t, map[string]string{"A.zip": "aaa", "B.zip": "bbbb", "C.zip": "ccccc"})
	root := t.TempDir()
	artworkDir := filepath.Join(root, "artwork")
	bezelDir := filepath.Join(root, "bezels")
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")

	// Prepare local box art for A and B (C intentionally missing locally).
	boxDir := filepath.Join(artworkDir, "Nintendo - Nintendo Entertainment System", "Named_Boxarts")
	os.MkdirAll(boxDir, 0o755)
	os.WriteFile(filepath.Join(boxDir, "A.png"), []byte("png-a"), 0o644)
	os.WriteFile(filepath.Join(boxDir, "B.png"), []byte("png-b"), 0o644)

	// Prepare all 6 console bezel PNGs + sources.yaml + LICENSE.
	os.MkdirAll(bezelDir, 0o755)
	sources := map[string]map[string]string{}
	for _, c := range library.Consoles {
		body := []byte("png-" + c.Slug)
		os.WriteFile(filepath.Join(bezelDir, c.Slug+".png"), body, 0o644)
		sources[c.Slug] = map[string]string{"sha256": util.SHA256Hex(body)}
	}
	util.WriteYAML(filepath.Join(bezelDir, "sources.yaml"), sources)
	os.WriteFile(filepath.Join(bezelDir, "LICENSE"), []byte("MIT"), 0o644)

	// Prepare config files.
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "apple-tv.cfg"), []byte(
		"input_joypad_driver = \"mfi\"\ninput_max_users = \"2\"\ninput_player1_analog_dpad_mode = \"1\"\ninput_player2_analog_dpad_mode = \"1\"\n",
	), 0o644)
	os.WriteFile(filepath.Join(configDir, "global.slangp"), []byte("shaders = \"1\"\n"), 0o644)

	srv := newMemory(map[string]string{
		"/downloads/ROMS/nes/Old.zip": "keep",
		"/config/retroarch.cfg":       "custom_keep = \"yes\"\ninput_max_users = \"8\"\n",
	})

	opts := SyncOptions{
		ExportDir: export, ArtworkDir: artworkDir, BezelDir: bezelDir,
		ConfigDir: configDir, StateDir: stateDir, Workers: 4,
		ROMs: true, Playlists: true, Artwork: true, Bezels: true, Config: true,
	}

	// Dry run must not write anything.
	dryOpts := opts
	dryOpts.DryRun = true
	if err := SyncAll(srv, dryOpts); err != nil || len(srv.writes) != 0 {
		t.Fatalf("dry-run wrote: err=%v writes=%v", err, srv.writes)
	}

	// Real parallel sync.
	if err := SyncAll(srv, opts); err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}
	got := srv.visible()
	if got["/downloads/ROMS/nes/A.zip"] != "aaa" || got["/downloads/ROMS/nes/Old.zip"] != "keep" {
		t.Fatalf("ROMs mismatch: %v", got)
	}
	if got["/playlists/Nintendo - Nintendo Entertainment System.lpl"] != `{"version":"1.5","items":[]}` {
		t.Fatalf("playlist missing")
	}
	if got["/thumbnails/Nintendo - Nintendo Entertainment System/Named_Boxarts/A.png"] != "png-a" {
		t.Fatalf("artwork missing")
	}
	if !strings.Contains(got["/config/Nestopia/nes.cfg"], `input_player1_analog_dpad_mode = "1"`) {
		t.Fatalf("NES override missing left-stick D-pad mode: %s", got["/config/Nestopia/nes.cfg"])
	}
	if !strings.Contains(got["/config/Mupen64Plus-Next/n64.cfg"], `input_player1_analog_dpad_mode = "0"`) {
		t.Fatalf("N64 override should disable left-stick D-pad mode: %s", got["/config/Mupen64Plus-Next/n64.cfg"])
	}
	if !strings.Contains(got["/config/mGBA/gba.cfg"], `input_player1_analog_dpad_mode = "1"`) ||
		!strings.Contains(got["/config/mGBA/gba.cfg"], `aspect_ratio_index = "7"`) {
		t.Fatalf("GBA override should enable left-stick D-pad mode and 3:2 aspect ratio: %s", got["/config/mGBA/gba.cfg"])
	}
	cfg := got["/config/retroarch.cfg"]
	if !strings.Contains(cfg, `custom_keep = "yes"`) || !strings.Contains(cfg, `input_max_users = "2"`) || !strings.Contains(cfg, `input_player2_analog_dpad_mode = "1"`) {
		t.Fatalf("merged config unexpected: %s", cfg)
	}
	if got["/config/global.slangp"] != "shaders = \"1\"\n" {
		t.Fatalf("shader preset missing")
	}

	// Repeat sync should do zero writes.
	srv.writes = nil
	if err := SyncAll(srv, opts); err != nil || len(srv.writes) != 0 {
		t.Fatalf("repeat sync wrote: err=%v writes=%v", err, srv.writes)
	}
}

func TestSyncStopsOnDifferentSizeAndBadHash(t *testing.T) {
	export := writeExport(t, map[string]string{"A.zip": "aaa"})
	srv := newMemory(map[string]string{"/downloads/ROMS/nes/A.zip": "x"})
	if err := SyncAll(srv, SyncOptions{ExportDir: export, ROMs: true, Workers: 2}); err == nil || !strings.Contains(err.Error(), "different size") {
		t.Fatalf("err = %v", err)
	}
	os.WriteFile(filepath.Join(export, "nes", "A.zip"), []byte("zzz"), 0o644)
	srv = newMemory(nil)
	if err := SyncAll(srv, SyncOptions{ExportDir: export, ROMs: true, Workers: 2}); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeConfigPreservesUnrelatedSettings(t *testing.T) {
	base := []byte("# header\nkeep_me = \"abc\"\ninput_max_users = \"8\"\n")
	frag := []byte("# comment\ninput_max_users = \"2\"\ninput_player2_analog_dpad_mode = \"1\"\n")
	got := string(MergeConfig(base, frag))
	want := "# header\nkeep_me = \"abc\"\ninput_max_users = \"2\"\ninput_player2_analog_dpad_mode = \"1\"\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDiffConfigReportsAddedAndModifiedKeys(t *testing.T) {
	base := []byte("keep = \"same\"\ninput_max_users = \"8\"\n")
	merged := MergeConfig(base, []byte("keep = \"same\"\ninput_max_users = \"2\"\ninput_player2_analog_dpad_mode = \"1\"\n"))
	diffs := DiffConfig(base, merged)
	if len(diffs) != 2 {
		t.Fatalf("got %d diffs (%+v), want 2", len(diffs), diffs)
	}
	if diffs[0].Key != "input_max_users" || diffs[0].Added || diffs[0].OldVal != `"8"` || diffs[0].NewVal != `"2"` {
		t.Fatalf("diffs[0] = %+v", diffs[0])
	}
	if diffs[1].Key != "input_player2_analog_dpad_mode" || !diffs[1].Added || diffs[1].NewVal != `"1"` {
		t.Fatalf("diffs[1] = %+v", diffs[1])
	}
}

func TestSyncOutputIsConciseByDefaultAndDetailedWhenVerbose(t *testing.T) {
	export := writeExport(t, map[string]string{"Super Mario Bros.zip": "aaa", "Zelda.zip": "bbbb"})
	root := t.TempDir()
	artworkDir := filepath.Join(root, "artwork")
	configDir := filepath.Join(root, "config")
	stateDir := filepath.Join(root, "state")

	boxDir := filepath.Join(artworkDir, "Nintendo - Nintendo Entertainment System", "Named_Boxarts")
	os.MkdirAll(boxDir, 0o755)
	os.WriteFile(filepath.Join(boxDir, "Super Mario Bros.png"), []byte("png-a"), 0o644)

	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "apple-tv.cfg"), []byte("input_max_users = \"2\"\nconfig_save_on_exit = \"false\"\n"), 0o644)

	srv := newMemory(map[string]string{
		"/config/retroarch.cfg": "input_max_users = \"8\"\n",
	})

	var concise strings.Builder
	opts := SyncOptions{
		ExportDir: export, ArtworkDir: artworkDir, ConfigDir: configDir, StateDir: stateDir,
		Workers: 2, DryRun: true, Out: &concise,
		ROMs: true, Playlists: true, Artwork: true, Config: true,
	}
	if err := SyncAll(srv, opts); err != nil {
		t.Fatalf("concise dry-run failed: %v", err)
	}
	out := concise.String()
	// Individual ROM and artwork titles must not clutter default output.
	if strings.Contains(out, "Super Mario Bros.zip") || strings.Contains(out, "Zelda.zip") || strings.Contains(out, "Super Mario Bros.png") {
		t.Fatalf("concise output should not list individual ROM/artwork titles:\n%s", out)
	}
	// Summary, per-console breakdown, playlist change, and config key diff must be present.
	for _, want := range []string{
		"Sync preview (dry-run)",
		"ROMs:      would upload 2",
		"nes",
		"Playlists: would sync 1",
		"Artwork:   would upload 1",
		"1 missing locally",
		"Config:    would update 1 file(s)",
		`~ input_max_users: "8" -> "2"`,
		`+ config_save_on_exit = "false"`,
		"Dry-run complete",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("concise output missing %q:\n%s", want, out)
		}
	}

	// Verbose mode should include individual file paths.
	var verbose strings.Builder
	opts.Verbose = true
	opts.Out = &verbose
	if err := SyncAll(srv, opts); err != nil {
		t.Fatalf("verbose dry-run failed: %v", err)
	}
	if !strings.Contains(verbose.String(), "Super Mario Bros.zip") || !strings.Contains(verbose.String(), "Super Mario Bros.png") {
		t.Fatalf("verbose output should list individual files:\n%s", verbose.String())
	}

	// After real sync, a subsequent dry-run should report everything up to date.
	opts.DryRun, opts.Verbose, opts.Out = false, false, io.Discard
	if err := SyncAll(srv, opts); err != nil {
		t.Fatalf("real sync failed: %v", err)
	}
	var upToDate strings.Builder
	opts.DryRun, opts.Out = true, &upToDate
	if err := SyncAll(srv, opts); err != nil {
		t.Fatalf("up-to-date dry-run failed: %v", err)
	}
	if !strings.Contains(upToDate.String(), "No changes needed (dry-run).") {
		t.Fatalf("expected no-changes summary:\n%s", upToDate.String())
	}
}

func TestConfigBackupAndRestoreRoundTrip(t *testing.T) {
	export := writeExport(t, map[string]string{"A.zip": "aaa"})
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	backupDir := filepath.Join(root, "backups")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "apple-tv.cfg"), []byte("input_max_users = \"2\"\n"), 0o644)
	os.WriteFile(filepath.Join(configDir, "global.slangp"), []byte("shaders = \"1\"\n"), 0o644)

	origCfg := "keep_me = \"yes\"\ninput_max_users = \"8\"\n"
	origShader := "shaders = \"0\"\n"
	srv := newMemory(map[string]string{
		"/config/retroarch.cfg": origCfg,
		"/config/global.slangp": origShader,
	})

	if err := SyncAll(srv, SyncOptions{
		ExportDir: export, ConfigDir: configDir, BackupDir: backupDir, Config: true,
	}); err != nil {
		t.Fatalf("SyncAll config failed: %v", err)
	}
	if !strings.Contains(srv.visible()["/config/retroarch.cfg"], `input_max_users = "2"`) {
		t.Fatalf("expected updated config, got %q", srv.visible()["/config/retroarch.cfg"])
	}

	// Verify timestamped local backup captured the original running files before sync.
	entries, err := os.ReadDir(backupDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 backup dir, got %v (err=%v)", entries, err)
	}
	snapDir := filepath.Join(backupDir, entries[0].Name())
	if b, err := os.ReadFile(filepath.Join(snapDir, "config", "retroarch.cfg")); err != nil || string(b) != origCfg {
		t.Fatalf("backed up retroarch.cfg = %q (err=%v)", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(snapDir, "config", "global.slangp")); err != nil || string(b) != origShader {
		t.Fatalf("backed up global.slangp = %q (err=%v)", b, err)
	}

	// Restore the backup and verify original running files are back on the server.
	if err := RestoreConfig(srv, snapDir, false); err != nil {
		t.Fatalf("RestoreConfig failed: %v", err)
	}
	if got := srv.visible()["/config/retroarch.cfg"]; got != origCfg {
		t.Fatalf("restored retroarch.cfg = %q, want %q", got, origCfg)
	}
	if got := srv.visible()["/config/global.slangp"]; got != origShader {
		t.Fatalf("restored global.slangp = %q, want %q", got, origShader)
	}
}

func TestSyncPreservesExistingArtworkMergesPCEOptAndReplacesBezelAsset(t *testing.T) {
	export := writeExport(t, map[string]string{"A.zip": "aaa"})
	// Add a PC Engine Street Fighter II entry so syncConfig merges pce-six-button.opt.
	pceDir := filepath.Join(export, "PC Engine")
	os.MkdirAll(pceDir, 0o755)
	os.WriteFile(filepath.Join(pceDir, "Street Fighter II' - Champion Edition.zip"), []byte("sf2"), 0o644)
	os.WriteFile(filepath.Join(pceDir, "selection.json"), []byte(fmt.Sprintf(
		`[{"title":"Street Fighter II' - Champion Edition","file":"Street Fighter II' - Champion Edition.zip","bytes":3,"export_sha256":%q}]`,
		util.SHA256Hex([]byte("sf2")),
	)), 0o644)
	os.WriteFile(filepath.Join(export, "summary.json"), []byte(
		`{"consoles":[{"console":"nes","selected":1},{"console":"PC Engine","selected":1}]}`,
	), 0o644)

	root := t.TempDir()
	artworkDir := filepath.Join(root, "artwork")
	bezelDir := filepath.Join(root, "bezels")
	configDir := filepath.Join(root, "config")
	backupDir := filepath.Join(root, "backups")

	boxDir := filepath.Join(artworkDir, "Nintendo - Nintendo Entertainment System", "Named_Boxarts")
	os.MkdirAll(boxDir, 0o755)
	os.WriteFile(filepath.Join(boxDir, "A.png"), []byte("new-longer-png"), 0o644)

	os.MkdirAll(bezelDir, 0o755)
	sources := map[string]map[string]string{}
	for _, c := range library.Consoles {
		body := []byte("png-" + c.Slug)
		os.WriteFile(filepath.Join(bezelDir, c.Slug+".png"), body, 0o644)
		sources[c.Slug] = map[string]string{"sha256": util.SHA256Hex(body)}
	}
	util.WriteYAML(filepath.Join(bezelDir, "sources.yaml"), sources)
	os.WriteFile(filepath.Join(bezelDir, "LICENSE"), []byte("MIT"), 0o644)

	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "apple-tv.cfg"), []byte("input_max_users = \"2\"\n"), 0o644)
	os.WriteFile(filepath.Join(configDir, "pce-six-button.opt"), []byte(
		"pce_fast_default_joypad_type_p1 = \"6 Buttons\"\npce_fast_default_joypad_type_p2 = \"6 Buttons\"\n",
	), 0o644)

	srv := newMemory(map[string]string{
		"/thumbnails/Nintendo - Nintendo Entertainment System/Named_Boxarts/A.png": "existing-art",
		"/overlays/console-bezels/LICENSE":                                         "MIT\n", // 1 byte longer than local "MIT"
		"/config/retroarch.cfg":                                                    "input_max_users = \"2\"\n",
		"/config/Beetle PCE Fast/Street Fighter II' - Champion Edition.opt":        "pce_fast_adpcmvolume = \"100\"\npce_fast_default_joypad_type_p1 = \"2 Buttons\"\n",
	})

	if err := SyncAll(srv, SyncOptions{
		ExportDir: export, ArtworkDir: artworkDir, BezelDir: bezelDir,
		ConfigDir: configDir, BackupDir: backupDir, Workers: 2,
		Artwork: true, Bezels: true, Config: true,
	}); err != nil {
		t.Fatalf("SyncAll failed: %v", err)
	}

	got := srv.visible()
	if got["/thumbnails/Nintendo - Nintendo Entertainment System/Named_Boxarts/A.png"] != "existing-art" {
		t.Fatalf("existing remote artwork should be preserved, got %q", got["/thumbnails/Nintendo - Nintendo Entertainment System/Named_Boxarts/A.png"])
	}
	if got["/overlays/console-bezels/LICENSE"] != "MIT" {
		t.Fatalf("bezel LICENSE should be safely replaced, got %q", got["/overlays/console-bezels/LICENSE"])
	}
	opt := got["/config/Beetle PCE Fast/Street Fighter II' - Champion Edition.opt"]
	if !strings.Contains(opt, `pce_fast_adpcmvolume = "100"`) ||
		!strings.Contains(opt, `pce_fast_default_joypad_type_p1 = "6 Buttons"`) ||
		!strings.Contains(opt, `pce_fast_default_joypad_type_p2 = "6 Buttons"`) {
		t.Fatalf("PCE opt should merge and preserve existing keys, got:\n%s", opt)
	}
}

func TestHTTPServerListUploadAndDownload(t *testing.T) {
	var uploaded []byte
	var folder string
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"name":"a b.zip","path":%q,"size":3},{"name":"d","path":%q}]`, r.URL.Query().Get("path")+"a b.zip", r.URL.Query().Get("path")+"d/")
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("abc")) })
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength <= 0 {
			http.Error(w, "length required", 411)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		folder = r.FormValue("path")
		f, _, _ := r.FormFile("files[]")
		uploaded, _ = io.ReadAll(f)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := NewServer(ts.URL + "/")
	list, err := s.List("/x/")
	if err != nil || list["a b.zip"].Size == nil || list["d"].Path != "/x/d/" {
		t.Fatalf("list = %v, %v", list, err)
	}
	var sb strings.Builder
	n, sum, _ := s.Download("/x/a b.zip", &sb)
	if n != 3 || sum != util.SHA256Hex([]byte("abc")) {
		t.Fatalf("download %d %s", n, sum)
	}
	local := filepath.Join(t.TempDir(), "r.bin")
	os.WriteFile(local, []byte("payload"), 0o644)
	if err := s.Upload("/x/", local, 7); err != nil || string(uploaded) != "payload" || folder != "/x/" {
		t.Fatalf("upload %q %q %v", uploaded, folder, err)
	}
}
