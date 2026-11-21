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

	"github.com/fabiogoma/retroarch/internal/util"
)

// memory mimics the RetroArch file server, including numbering of duplicate uploads.
type memory struct {
	mu            sync.Mutex
	files         map[string][]byte
	writes        []string
	corruptUpload bool
	failMove      func(old, new string) bool
	failDownload  func(remote string) bool
	hook          func(remote string)
}

func newMemory(files map[string]string) *memory {
	m := &memory{files: map[string][]byte{}}
	for k, v := range files {
		m.files[k] = []byte(v)
	}
	return m
}

func (m *memory) URL() string { return "memory://" }

func (m *memory) dirs() map[string]bool {
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
	if !m.dirs()[folder] {
		return nil, fmt.Errorf("HTTP 404")
	}
	for p, data := range m.files {
		if strings.HasPrefix(p, folder) {
			rest := p[len(folder):]
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
	defer m.mu.Unlock()
	if m.failDownload != nil && m.failDownload(remote) {
		return 0, "", errors.New("Backup download interrupted")
	}
	if m.hook != nil {
		m.hook(remote)
	}
	data, ok := m.files[remote]
	if !ok {
		return 0, "", errors.New("HTTP 404")
	}
	w.Write(data)
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:]), nil
}

func (m *memory) Move(oldPath, newPath string) error {
	m.writes = append(m.writes, "move")
	if m.failMove != nil && m.failMove(oldPath, newPath) {
		return errors.New("Rename interrupted")
	}
	m.files[newPath] = m.files[oldPath]
	delete(m.files, oldPath)
	return nil
}

func (m *memory) EnsureFolder(parts []string, dryRun bool) (string, error) {
	folder := "/"
	for _, name := range parts {
		child := folder + name + "/"
		if _, isFile := m.files[folder+name]; isFile {
			return "", fmt.Errorf("Remote path is not a directory: %s", child)
		}
		if !m.dirs()[child] {
			if dryRun {
				return "", nil
			}
			m.writes = append(m.writes, "create")
			m.files[child+".dir"] = nil // placeholder so empty folders exist
		}
		folder = child
	}
	return folder, nil
}

func (m *memory) Upload(folder, localPath string, size int64) error {
	m.writes = append(m.writes, "upload")
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	if m.corruptUpload {
		data = append([]byte{}, data...)
		data[0] ^= 0xff
	}
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
	var sel string
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		os.WriteFile(filepath.Join(folder, n), []byte(files[n]), 0o644)
		if i > 0 {
			sel += ","
		}
		sel += fmt.Sprintf(`{"file":%q,"bytes":%d,"export_sha256":%q}`, n, len(files[n]), util.SHA256Hex([]byte(files[n])))
	}
	os.WriteFile(filepath.Join(folder, "selection.json"), []byte("["+sel+"]"), 0o644)
	os.WriteFile(filepath.Join(export, "summary.json"), []byte(fmt.Sprintf(`{"consoles":[{"console":"nes","selected":%d}]}`, len(files))), 0o644)
	return export
}

func TestSyncIsAdditiveAndRepeatable(t *testing.T) {
	export := writeExport(t, map[string]string{"A.zip": "aaa", "B.zip": "bbbb"})
	groups, err := LoadExport(export)
	if err != nil {
		t.Fatal(err)
	}
	srv := newMemory(map[string]string{"/downloads/ROMS/nes/Old.zip": "keep"})
	if err := Sync(srv, groups, true); err != nil || len(srv.writes) != 0 {
		t.Fatalf("dry-run wrote: %v %v", err, srv.writes)
	}
	if err := Sync(srv, groups, false); err != nil {
		t.Fatal(err)
	}
	got := srv.visible()
	if got["/downloads/ROMS/nes/A.zip"] != "aaa" || got["/downloads/ROMS/nes/Old.zip"] != "keep" {
		t.Fatalf("remote = %v", got)
	}
	srv.writes = nil
	if err := Sync(srv, groups, false); err != nil || len(srv.writes) != 0 {
		t.Fatalf("repeat sync wrote: %v %v", err, srv.writes)
	}
}

func TestSyncStopsOnDifferentSizeAndBadHash(t *testing.T) {
	export := writeExport(t, map[string]string{"A.zip": "aaa"})
	groups, _ := LoadExport(export)
	srv := newMemory(map[string]string{"/downloads/ROMS/nes/A.zip": "x"})
	if err := Sync(srv, groups, false); err == nil || !strings.Contains(err.Error(), "different size") {
		t.Fatalf("err = %v", err)
	}
	os.WriteFile(filepath.Join(export, "nes", "A.zip"), []byte("zzz"), 0o644)
	srv = newMemory(nil)
	if err := Sync(srv, groups, false); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadExportRejectsUnsafeNames(t *testing.T) {
	export := writeExport(t, map[string]string{"A.zip": "aaa"})
	os.WriteFile(filepath.Join(export, "nes", "selection.json"), []byte(`[{"file":"../x.zip","bytes":1,"export_sha256":""}]`), 0o644)
	if _, err := LoadExport(export); err == nil {
		t.Fatal("expected unsafe name error")
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

type profileFixture struct {
	t       *testing.T
	server  *memory
	profile string
}

func newProfile(t *testing.T) *profileFixture {
	return &profileFixture{t, newMemory(map[string]string{
		"/config/retroarch.cfg":                         "video_shader_enable = \"true\"\n",
		"/downloads/ROMS/nes/Mickey #1.zip":             "rom",
		"/playlists/Nintendo.lpl":                       "{}",
		"/saves/Mickey.srm":                             "save",
		"/thumbnails/Nintendo/Named_Boxarts/Mickey.png": "png",
	}), filepath.Join(t.TempDir(), "setup")}
}

func (f *profileFixture) capture(saves bool) error {
	return Backup(f.server, BackupOptions{Profile: f.profile, IncludeSaves: saves})
}

func TestBackupRestoreRoundTripAndSavesOptIn(t *testing.T) {
	f := newProfile(t)
	if err := f.capture(false); err != nil {
		t.Fatal(err)
	}
	m, err := LoadProfile(f.profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files["saves/Mickey.srm"]; ok {
		t.Fatal("saves included without opt-in")
	}
	target := newMemory(nil)
	if err := Restore(target, f.profile, true); err != nil || len(target.writes) != 0 {
		t.Fatalf("dry run: %v %v", err, target.writes)
	}
	if err := Restore(target, f.profile, false); err != nil {
		t.Fatal(err)
	}
	for p, want := range f.server.visible() {
		if strings.HasPrefix(p, "/saves/") {
			continue
		}
		if got := target.visible()[p]; got != want {
			t.Fatalf("%s = %q, want %q", p, got, want)
		}
	}
	if err := f.capture(true); err != nil {
		t.Fatal(err)
	}
	if m, _ := LoadProfile(f.profile); m.Files["saves/Mickey.srm"].Bytes == 0 {
		t.Fatal("saves missing with opt-in")
	}
}

func TestSecondBackupKeepsPreviousAndFailureKeepsProfile(t *testing.T) {
	f := newProfile(t)
	f.capture(false)
	f.server.files["/config/retroarch.cfg"] = []byte("new")
	f.capture(false)
	prev, _ := os.ReadFile(filepath.Join(f.profile+".previous", "files/config/retroarch.cfg"))
	if string(prev) != "video_shader_enable = \"true\"\n" {
		t.Fatalf("previous = %q", prev)
	}
	f.server.failDownload = func(r string) bool { return strings.HasSuffix(r, ".zip") }
	if err := f.capture(false); err == nil {
		t.Fatal("expected failure")
	}
	if cur, _ := os.ReadFile(filepath.Join(f.profile, "files/config/retroarch.cfg")); string(cur) != "new" {
		t.Fatalf("profile changed: %q", cur)
	}
	if _, err := LoadProfile(f.profile); err != nil {
		t.Fatal(err)
	}
}

func TestSameSizeChangedRemoteIsReplacedAndBackedUp(t *testing.T) {
	f := newProfile(t)
	f.capture(false)
	target := newMemory(map[string]string{"/downloads/ROMS/nes/Mickey #1.zip": "BAD"})
	if err := Restore(target, f.profile, false); err != nil {
		t.Fatal(err)
	}
	vis := target.visible()
	if vis["/downloads/ROMS/nes/Mickey #1.zip"] != "rom" {
		t.Fatal("not replaced")
	}
	found := false
	for p, d := range vis {
		found = found || strings.Contains(p, ".restore-backup-") && d == "BAD"
	}
	if !found {
		t.Fatal("original not preserved")
	}
}

func TestRestoreFailuresKeepOriginal(t *testing.T) {
	f := newProfile(t)
	f.capture(false)

	target := newMemory(map[string]string{"/config/retroarch.cfg": "original"})
	target.corruptUpload = true
	if err := Restore(target, f.profile, false); err == nil || !strings.Contains(err.Error(), "Staged upload verification failed") {
		t.Fatalf("err = %v", err)
	}
	if string(target.files["/config/retroarch.cfg"]) != "original" {
		t.Fatal("original changed")
	}
	for _, w := range target.writes {
		if w == "move" {
			t.Fatal("moved after bad upload")
		}
	}

	target = newMemory(map[string]string{"/config/retroarch.cfg": "original"})
	target.failMove = func(old, new string) bool {
		return strings.Contains(old, "/restore-") && new == "/config/retroarch.cfg"
	}
	if err := Restore(target, f.profile, false); err == nil || !strings.Contains(err.Error(), "Rename interrupted") {
		t.Fatalf("err = %v", err)
	}
	if string(target.files["/config/retroarch.cfg"]) != "original" {
		t.Fatal("original not rolled back")
	}

	target = newMemory(map[string]string{"/config/retroarch.cfg": "original"})
	target.failDownload = func(r string) bool { return strings.Contains(r, ".restore-backup-") }
	if err := Restore(target, f.profile, false); err == nil {
		t.Fatal("expected failure")
	}
	if string(target.files["/config/retroarch.cfg"]) != "original" {
		t.Fatal("original not rolled back after backup failure")
	}
}

func TestDirectoryConflictStopsBeforeWrites(t *testing.T) {
	f := newProfile(t)
	f.capture(false)
	target := newMemory(map[string]string{"/downloads": "conflict"})
	if err := Restore(target, f.profile, false); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v", err)
	}
	if len(target.writes) != 0 {
		t.Fatalf("writes = %v", target.writes)
	}
}

func TestModifiedProfileTraversalAndSymlinkAreRefused(t *testing.T) {
	f := newProfile(t)
	f.capture(false)
	rom := filepath.Join(f.profile, "files/downloads/ROMS/nes/Mickey #1.zip")
	os.WriteFile(rom, []byte("BAD"), 0o644)
	if _, err := LoadProfile(f.profile); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v", err)
	}
	f.capture(false) // fails: previous profile is untouched, so recapture first
	os.WriteFile(rom, []byte("rom"), 0o644)
	mp := filepath.Join(f.profile, "manifest.json")
	orig, _ := os.ReadFile(mp)
	var m Manifest
	util.ReadJSON(mp, &m)
	m.Files["../escape"] = FileEntry{}
	util.WriteJSON(mp, m)
	if _, err := LoadProfile(f.profile); err == nil || !strings.Contains(err.Error(), "Unsafe") {
		t.Fatalf("err = %v", err)
	}
	os.WriteFile(mp, orig, 0o644)
	cfg := filepath.Join(f.profile, "files/config/retroarch.cfg")
	outside := filepath.Join(t.TempDir(), "outside.cfg")
	os.Rename(cfg, outside)
	os.Symlink(outside, cfg)
	if _, err := LoadProfile(f.profile); err == nil || !strings.Contains(err.Error(), "Symlinks") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalSourceAndApplePortability(t *testing.T) {
	source := t.TempDir()
	home := "/private/var/mobile/Containers/Data/Application/01234567-89AB-CDEF-0123-456789ABCDEF"
	bundle := "/private/var/containers/Bundle/Application/01234567-89AB-CDEF-0123-456789ABCDEF/RetroArch.app"
	os.WriteFile(filepath.Join(source, "retroarch.cfg"), []byte(`thumbnails_directory = "`+home+`/Library/Caches/RetroArch/thumbnails"`+"\n"), 0o644)
	os.Mkdir(filepath.Join(source, "playlists"), 0o755)
	os.WriteFile(filepath.Join(source, "playlists", "test.lpl"), []byte(`{"core_path":"`+bundle+`/Frameworks/nestopia.libretro.framework"}`), 0o644)
	profile := filepath.Join(t.TempDir(), "setup")
	if err := Backup(nil, BackupOptions{Profile: profile, Source: source}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfile(profile); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(profile, "files/config/retroarch.cfg"))
	lpl, _ := os.ReadFile(filepath.Join(profile, "files/playlists/test.lpl"))
	orig, _ := os.ReadFile(filepath.Join(source, "retroarch.cfg"))
	if !strings.Contains(string(cfg), `"~/Library/Caches/RetroArch/thumbnails"`) || !strings.Contains(string(lpl), `":/Frameworks/`) || !strings.Contains(string(orig), home) {
		t.Fatalf("cfg=%s lpl=%s", cfg, lpl)
	}
}

func TestBackupRequiresMainConfig(t *testing.T) {
	f := newProfile(t)
	delete(f.server.files, "/config/retroarch.cfg")
	if err := f.capture(false); err == nil || !strings.Contains(err.Error(), "no config/retroarch.cfg") {
		t.Fatalf("err = %v", err)
	}
}

func TestChangingHistoryIsRetriedWithFreshSize(t *testing.T) {
	f := newProfile(t)
	remote := "/playlists/builtin/content_history.lpl"
	f.server.files[remote] = []byte("old")
	calls := 0
	f.server.hook = func(r string) {
		if r == remote {
			calls++
			f.server.files[r] = []byte("updated history")
		}
	}
	if err := f.capture(false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.profile, "files"+remote))
	if calls != 2 || string(got) != "updated history" {
		t.Fatalf("calls=%d got=%q", calls, got)
	}
}

func TestConfigChangeDuringCapturePreservesPreviousProfile(t *testing.T) {
	f := newProfile(t)
	f.capture(false)
	before, _ := os.ReadFile(filepath.Join(f.profile, "manifest.json"))
	configCopied := make(chan struct{})
	var once sync.Once
	f.server.hook = func(r string) {
		switch {
		case r == "/config/retroarch.cfg":
			// Closed after this call returns the old contents.
			defer once.Do(func() { close(configCopied) })
		case strings.HasSuffix(r, "Mickey.png"):
			f.server.mu.Unlock()
			<-configCopied
			f.server.mu.Lock()
			f.server.files["/config/retroarch.cfg"] = []byte("new settings")
		}
	}
	err := f.capture(false)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(f.profile, "manifest.json"))
	if string(before) != string(after) {
		t.Fatal("previous profile modified")
	}
}

func TestPushConfigReplacesAndBacksUp(t *testing.T) {
	m := newMemory(map[string]string{"/" + mainConfig: "old=1\n"})
	dir := t.TempDir()
	local := filepath.Join(dir, "retroarch.cfg")
	if err := os.WriteFile(local, []byte("new=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PushConfig(m, local, filepath.Join(dir, "b"), true); err != nil || m.visible()["/"+mainConfig] != "old=1\n" {
		t.Fatalf("dry run changed device: %v", err)
	}
	if err := PushConfig(m, local, filepath.Join(dir, "b"), false); err != nil {
		t.Fatal(err)
	}
	v := m.visible()
	if v["/"+mainConfig] != "new=2\n" {
		t.Fatalf("config not replaced: %q", v["/"+mainConfig])
	}
	kept := false
	for n, c := range v {
		if n != "/"+mainConfig && c == "old=1\n" {
			kept = true
		}
	}
	if !kept {
		t.Fatal("remote backup missing")
	}
	if backups, _ := filepath.Glob(filepath.Join(dir, "b", "*.cfg")); len(backups) != 1 {
		t.Fatal("local backup missing")
	}
}
