package fetch

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, payload []byte, deny bool) {
	t.Helper()
	sum := sha1.Sum(payload)
	mux := http.NewServeMux()
	mux.HandleFunc("/metadata/item", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"files":[{"name":"dir/Game.nes","size":"%d","sha1":"%s"}]}`, len(payload), hex.EncodeToString(sum[:]))
	})
	mux.HandleFunc("/download/item/dir/Game.nes", func(w http.ResponseWriter, r *http.Request) {
		if deny {
			http.Error(w, "no", 403)
			return
		}
		w.Write(payload)
	})
	ts := httptest.NewServer(mux)
	old := ArchiveBase
	ArchiveBase = ts.URL
	t.Cleanup(func() { ArchiveBase = old; ts.Close() })
}

func dests(t *testing.T) Destinations {
	root := t.TempDir()
	return Destinations{Consoles: map[string]bool{"nes": true}, Root: root, ROMs: filepath.Join(root, "ROMs")}
}

const manifest = `{"version":1,"files":[{"item":"item","file":"dir/Game.nes","destination":"nes/Game.nes"}]}`

func TestManifestDownloadVerifiesAndDoesNotClobber(t *testing.T) {
	archive(t, []byte("rom data"), false)
	d := dests(t)
	plan, err := ArchivePlan([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := Download(plan, d, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d.ROMs, "nes", "Game.nes")); err == nil {
		t.Fatal("dry run wrote a file")
	}
	if err := Download(plan, d, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(d.ROMs, "nes", "Game.nes")); string(got) != "rom data" {
		t.Fatalf("got %q", got)
	}
	if err := Download(plan, d, false); err != nil {
		t.Fatalf("existing verified file rejected: %v", err)
	}
	os.WriteFile(filepath.Join(d.ROMs, "nes", "Game.nes"), []byte("other!!!"), 0o644)
	if err := Download(plan, d, false); err == nil || !strings.Contains(err.Error(), "Checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestAccessDeniedAndTruncatedAreRejected(t *testing.T) {
	archive(t, []byte("rom data"), true)
	plan, _ := ArchivePlan([]byte(manifest))
	d := dests(t)
	err := Download(plan, d, false)
	if _, ok := err.(AccessDenied); !ok {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(d.ROMs, "nes")); len(entries) != 0 {
		t.Fatalf("leftovers: %v", entries)
	}
	plan[0].Size = 3
	archive(t, []byte("rom data"), false)
	plan[0].URL = ArchiveBase + "/download/item/dir/Game.nes"
	if err := Download(plan, d, false); err == nil {
		t.Fatal("expected size error")
	}
}

func TestDestinationValidation(t *testing.T) {
	d := dests(t)
	for _, bad := range []string{"../x.nes", "/abs.nes", "nes/../../x", "unknown/x.nes", "rom_library/bezels/sub/x.png", "rom_library/bezels/x.txt", "nes/a:b"} {
		if _, err := d.resolve(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"nes/x.nes", "artwork/x.png", "rom_library/bezels/x.png"} {
		if _, err := d.resolve(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	os.MkdirAll(d.ROMs, 0o755)
	os.Symlink(t.TempDir(), filepath.Join(d.ROMs, "nes"))
	if _, err := d.resolve("nes/x.nes"); err == nil {
		t.Error("symlink accepted")
	}
}

func TestDuplicateDestinationsAndBadItems(t *testing.T) {
	d := dests(t)
	p := Plan{URL: "x", Destination: "nes/A.nes", Size: 0, Hashes: map[string]string{}}
	q := p
	q.Destination = "nes/a.NES"
	if err := Download([]Plan{p, q}, d, true); err == nil || !strings.Contains(err.Error(), "Duplicate") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Metadata("../etc"); err == nil {
		t.Fatal("bad item accepted")
	}
	if _, err := ArchivePlan([]byte(`{"version":1,"files":[]}`)); err == nil {
		t.Fatal("empty manifest accepted")
	}
}

func TestBezelPlanRejectsUnexpectedSource(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bezels"), 0o755)
	sha := strings.Repeat("a", 64)
	os.WriteFile(filepath.Join(dir, "bezels", "sources.json"), []byte(`{"nes":{"source":"https://evil.example/x.png","sha256":"`+sha+`"}}`), 0o644)
	if _, err := BezelPlan(dir); err == nil {
		t.Fatal("expected rejection")
	}
	os.WriteFile(filepath.Join(dir, "bezels", "sources.json"), []byte(`{"nes":{"source":"https://raw.githubusercontent.com/libretro/overlay-borders/x.png","sha256":"`+sha+`"}}`), 0o644)
	plan, err := BezelPlan(dir)
	if err != nil || plan[0].Destination != "rom_library/bezels/nes.png" {
		t.Fatalf("%v %v", plan, err)
	}
}
