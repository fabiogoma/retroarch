package artwork

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/util"
)

func TestMatchBoxArtPrefersExactStemAndUSA(t *testing.T) {
	avail := []string{
		"Super Mario Bros. 3 (Europe).png",
		"Super Mario Bros. 3 (Japan).png",
		"Super Mario Bros. 3 (USA).png",
	}
	p := library.Selection{
		Entry:  library.Entry{Title: "Super Mario Bros. 3"},
		Source: "/roms/nes/Super Mario Bros. 3 (USA).nes",
	}
	if got := MatchBoxArt(p, avail); got != "Super Mario Bros. 3 (USA).png" {
		t.Fatalf("got %q", got)
	}
	// Normalized fallback without exact stem
	p2 := library.Selection{Entry: library.Entry{Title: "Super Mario Bros. 3"}}
	if got := MatchBoxArt(p2, avail); got != "Super Mario Bros. 3 (USA).png" {
		t.Fatalf("fallback got %q", got)
	}
}

func TestDownloadBoxArtAndBezelsInParallel(t *testing.T) {
	fakePNG := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("fake-image-body")...)
	bezelBytes := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("bezel-body")...)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/libretro-thumbnails/Nintendo_-_Nintendo_Entertainment_System/git/trees/master", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tree":[{"path":"Named_Boxarts","sha":"boxsha","type":"tree"}]}`)
	})
	mux.HandleFunc("/repos/libretro-thumbnails/Nintendo_-_Nintendo_Entertainment_System/git/trees/boxsha", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tree":[{"path":"Super Mario Bros. 3 (USA).png","type":"blob"},{"path":"DuckTales (USA).png","type":"blob"}]}`)
	})
	mux.HandleFunc("/libretro-thumbnails/Nintendo_-_Nintendo_Entertainment_System/master/Named_Boxarts/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fakePNG)
	})
	mux.HandleFunc("/bezel.png", func(w http.ResponseWriter, r *http.Request) {
		w.Write(bezelBytes)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	origAPI, origRaw := GitHubAPIBase, GitHubRawBase
	GitHubAPIBase, GitHubRawBase = ts.URL, ts.URL
	defer func() { GitHubAPIBase, GitHubRawBase = origAPI, origRaw }()

	root := t.TempDir()
	exportDir := filepath.Join(root, "export")
	artworkDir := filepath.Join(root, "artwork")
	bezelDir := filepath.Join(root, "bezels")
	os.MkdirAll(filepath.Join(exportDir, "nes"), 0o755)
	os.MkdirAll(bezelDir, 0o755)

	os.WriteFile(filepath.Join(exportDir, "summary.json"), []byte(`{"consoles":[{"console":"nes","selected":2}]}`), 0o644)
	os.WriteFile(filepath.Join(exportDir, "nes", "selection.json"), []byte(`[
		{"title":"Super Mario Bros. 3","file":"Super Mario Bros. 3.zip"},
		{"title":"DuckTales","file":"DuckTales.zip"}
	]`), 0o644)
	sourcesYAML := fmt.Sprintf("nes:\n  source: %s/bezel.png\n  sha256: %s\n", ts.URL, util.SHA256Hex(bezelBytes))
	os.WriteFile(filepath.Join(bezelDir, "sources.yaml"), []byte(sourcesYAML), 0o644)

	if err := Download(Options{
		ExportDir:  exportDir,
		ArtworkDir: artworkDir,
		BezelDir:   bezelDir,
		Workers:    4,
	}); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	for _, title := range []string{"Super Mario Bros. 3", "DuckTales"} {
		p := BoxArtPath(artworkDir, "Nintendo - Nintendo Entertainment System", title)
		if got, err := os.ReadFile(p); err != nil || string(got) != string(fakePNG) {
			t.Fatalf("box art %s: err=%v len=%d", p, err, len(got))
		}
	}
	if got, err := os.ReadFile(filepath.Join(bezelDir, "nes.png")); err != nil || string(got) != string(bezelBytes) {
		t.Fatalf("bezel: err=%v len=%d", err, len(got))
	}
}

func TestDownloadPNGFollowsGitSymlink(t *testing.T) {
	fakePNG := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("dr-mario-png")...)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repo/master/Named_Boxarts/Dr. Mario (Japan, USA) (En) (Rev 1).png":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "Dr. Mario (Japan, USA).png")
		case "/repo/master/Named_Boxarts/Dr. Mario (Japan, USA).png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(fakePNG)
		case "/repo/master/Named_Boxarts/loop-a.png":
			fmt.Fprint(w, "loop-b.png")
		case "/repo/master/Named_Boxarts/loop-b.png":
			fmt.Fprint(w, "loop-a.png")
		case "/repo/master/Named_Boxarts/bad.png":
			fmt.Fprint(w, "<html>not a png</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "Dr. Mario.png")
	symlinkURL := ts.URL + "/repo/master/Named_Boxarts/Dr.%20Mario%20%28Japan%2C%20USA%29%20%28En%29%20%28Rev%201%29.png"
	if err := downloadPNG(context.Background(), ts.Client(), symlinkURL, dest); err != nil {
		t.Fatalf("downloadPNG symlink failed: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != string(fakePNG) {
		t.Fatalf("downloaded PNG mismatch: err=%v got=%q", err, string(got))
	}

	if err := downloadPNG(context.Background(), ts.Client(), ts.URL+"/repo/master/Named_Boxarts/loop-a.png", dest); err == nil || !strings.Contains(err.Error(), "not a valid PNG") {
		t.Fatalf("expected symlink loop error, got %v", err)
	}
	if err := downloadPNG(context.Background(), ts.Client(), ts.URL+"/repo/master/Named_Boxarts/bad.png", dest); err == nil || !strings.Contains(err.Error(), "not a valid PNG") {
		t.Fatalf("expected invalid PNG error, got %v", err)
	}
}
