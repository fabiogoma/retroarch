// Package artwork downloads game box art from Libretro's official thumbnail repositories
// and hash-pinned console bezel PNGs into the local workspace.
package artwork

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/util"
)

// Tunables set from configuration by the cmd package.
var (
	UserAgent       = "RetroArchAppleTV/1.0"
	ResponseTimeout = 120 * time.Second
	GitHubAPIBase   = "https://api.github.com"
	GitHubRawBase   = "https://raw.githubusercontent.com"
)

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// Options configures an artwork + bezel download run.
type Options struct {
	ExportDir  string
	ArtworkDir string
	BezelDir   string
	Workers    int
	DryRun     bool
}

// Download fetches missing box art for every game in ExportDir and any missing
// console bezel PNGs listed in BezelDir/sources.yaml, running index fetches,
// bezel downloads, and box art downloads concurrently using goroutines.
func Download(o Options) error {
	if o.Workers < 1 {
		o.Workers = 4
	}
	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: ResponseTimeout,
			MaxIdleConnsPerHost:   o.Workers + 4,
		},
	}

	if err := downloadBezels(client, o.BezelDir, o.Workers, o.DryRun); err != nil {
		return err
	}

	selections, err := library.LoadExportSelections(o.ExportDir)
	if err != nil {
		return err
	}

	type consoleWork struct {
		console library.ConsoleInfo
		need    []library.Selection
	}
	var (
		work    []consoleWork
		skipped int
	)
	for _, c := range library.Consoles {
		picks := selections[c.Folder]
		if len(picks) == 0 {
			continue
		}
		var need []library.Selection
		for _, p := range picks {
			dest := BoxArtPath(o.ArtworkDir, c.System, p.Title)
			if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				skipped++
				continue
			}
			need = append(need, p)
		}
		if len(need) > 0 {
			work = append(work, consoleWork{console: c, need: need})
		}
	}

	// Fetch GitHub Named_Boxarts indices for all needed consoles in parallel.
	indices := make([][]string, len(work))
	if _, err := runParallel(o.Workers, len(work), func(ctx context.Context, i int) error {
		avail, err := fetchBoxArtIndex(ctx, client, work[i].console.LibretroRepo)
		if err != nil {
			return fmt.Errorf("fetch box art index for %s: %w", work[i].console.System, err)
		}
		indices[i] = avail
		return nil
	}); err != nil {
		return err
	}

	type dlJob struct {
		url   string
		dest  string
		label string
	}
	var (
		jobs    []dlJob
		missing []string
	)
	for i, w := range work {
		for _, p := range w.need {
			match := MatchBoxArt(p, indices[i])
			if match == "" {
				missing = append(missing, fmt.Sprintf("%s: %s", w.console.Folder, p.Title))
				continue
			}
			rawURL := fmt.Sprintf("%s/libretro-thumbnails/%s/master/Named_Boxarts/%s",
				strings.TrimRight(GitHubRawBase, "/"), w.console.LibretroRepo, url.PathEscape(match))
			jobs = append(jobs, dlJob{
				url:   rawURL,
				dest:  BoxArtPath(o.ArtworkDir, w.console.System, p.Title),
				label: w.console.Folder + "/" + p.Title,
			})
		}
	}

	if o.DryRun {
		for _, j := range jobs {
			fmt.Printf("Would download box art: %s -> %s\n", j.label, j.dest)
		}
		fmt.Printf("Would download %d box arts (%d already local, %d unmatched).\n", len(jobs), skipped, len(missing))
		printMissing(missing)
		return nil
	}

	downloaded, err := runParallel(o.Workers, len(jobs), func(ctx context.Context, i int) error {
		j := jobs[i]
		if err := downloadPNG(ctx, client, j.url, j.dest); err != nil {
			return fmt.Errorf("%s: %w", j.label, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("Downloaded %d box arts (%d already local, %d unmatched).\n", downloaded, skipped, len(missing))
	printMissing(missing)
	return nil
}

// BoxArtPath returns the local path for a game's Named_Boxarts PNG.
func BoxArtPath(artworkDir, system, title string) string {
	return filepath.Join(artworkDir, system, "Named_Boxarts", library.SanitizeThumbnailName(title)+".png")
}

func printMissing(missing []string) {
	if len(missing) == 0 {
		return
	}
	fmt.Println("Unmatched box art (skipped):")
	for _, m := range missing {
		fmt.Println("  -", m)
	}
}

func fetchBoxArtIndex(ctx context.Context, client *http.Client, repo string) ([]string, error) {
	rootURL := fmt.Sprintf("%s/repos/libretro-thumbnails/%s/git/trees/master", strings.TrimRight(GitHubAPIBase, "/"), repo)
	var rootTree struct {
		Tree []struct {
			Path string `json:"path"`
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := getJSON(ctx, client, rootURL, &rootTree); err != nil {
		return nil, err
	}
	var boxArtSHA string
	for _, entry := range rootTree.Tree {
		if entry.Path == "Named_Boxarts" && entry.Type == "tree" {
			boxArtSHA = entry.SHA
			break
		}
	}
	if boxArtSHA == "" {
		return nil, fmt.Errorf("Named_Boxarts tree not found in %s", repo)
	}
	subURL := fmt.Sprintf("%s/repos/libretro-thumbnails/%s/git/trees/%s", strings.TrimRight(GitHubAPIBase, "/"), repo, boxArtSHA)
	var subTree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := getJSON(ctx, client, subURL, &subTree); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(subTree.Tree))
	for _, entry := range subTree.Tree {
		if entry.Type == "blob" && strings.HasSuffix(strings.ToLower(entry.Path), ".png") {
			names = append(names, entry.Path)
		}
	}
	sort.Strings(names)
	return names, nil
}

// MatchBoxArt picks the best matching Named_Boxarts PNG filename for a selected game.
func MatchBoxArt(p library.Selection, available []string) string {
	// 1. Exact ROM stem match (e.g., No-Intro "Super Mario Bros. 3 (USA).png").
	stemSet := map[string]bool{}
	for _, src := range []string{p.ArchiveMember, p.Source} {
		if src == "" {
			continue
		}
		base := filepath.Base(src)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if stem != "" {
			stemSet[strings.ToLower(library.SanitizeThumbnailName(stem)+".png")] = true
		}
	}
	for _, name := range available {
		if stemSet[strings.ToLower(name)] {
			return name
		}
	}

	// 2. Normalized title / alias match ranked by region preference.
	keys := map[string]bool{library.Normalized(p.Title): true}
	for _, a := range p.Aliases {
		keys[library.Normalized(a)] = true
	}
	var bestName string
	bestScore := -1000
	for _, name := range available {
		title := library.TitleOf(name)
		if !keys[library.Normalized(title)] {
			continue
		}
		score := library.RegionScore(name)
		if score <= -100 || (score < 0 && !p.AllowJapanese) {
			continue
		}
		if bestName == "" || score > bestScore || (score == bestScore && name < bestName) {
			bestName = name
			bestScore = score
		}
	}
	return bestName
}

func downloadBezels(client *http.Client, bezelDir string, workers int, dryRun bool) error {
	var sources map[string]struct {
		Source string `yaml:"source"`
		SHA256 string `yaml:"sha256"`
	}
	if err := util.ReadYAML(filepath.Join(bezelDir, "sources.yaml"), &sources); err != nil {
		return err
	}
	slugs := make([]string, 0, len(sources))
	for s := range sources {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)

	type bezelJob struct {
		slug   string
		url    string
		sha256 string
		dest   string
	}
	var jobs []bezelJob
	for _, slug := range slugs {
		src := sources[slug]
		dest := filepath.Join(bezelDir, slug+".png")
		if data, err := os.ReadFile(dest); err == nil {
			if util.SHA256Hex(data) == strings.ToLower(src.SHA256) {
				continue
			}
		}
		if dryRun {
			fmt.Printf("Would download bezel: %s -> %s\n", slug, dest)
			continue
		}
		jobs = append(jobs, bezelJob{slug: slug, url: src.Source, sha256: strings.ToLower(src.SHA256), dest: dest})
	}
	if dryRun || len(jobs) == 0 {
		return nil
	}
	_, err := runParallel(workers, len(jobs), func(ctx context.Context, i int) error {
		j := jobs[i]
		data, err := getBytes(ctx, client, j.url)
		if err != nil {
			return fmt.Errorf("download bezel %s: %w", j.slug, err)
		}
		if util.SHA256Hex(data) != j.sha256 {
			return fmt.Errorf("bezel %s checksum mismatch", j.slug)
		}
		if err := writeAtomic(j.dest, data); err != nil {
			return err
		}
		fmt.Printf("Downloaded bezel: %s\n", j.dest)
		return nil
	})
	return err
}

func downloadPNG(ctx context.Context, client *http.Client, rawURL, dest string) error {
	data, err := getBytes(ctx, client, rawURL)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(data, pngMagic) {
		return fmt.Errorf("response is not a valid PNG: %s", rawURL)
	}
	return writeAtomic(dest, data)
}

func writeAtomic(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dl-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func getJSON(ctx context.Context, client *http.Client, rawURL string, dst any) error {
	data, err := getBytes(ctx, client, rawURL)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func getBytes(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && strings.HasPrefix(rawURL, GitHubAPIBase) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	const maxAssetBytes = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAssetBytes {
		return nil, fmt.Errorf("response too large: %s", rawURL)
	}
	return data, nil
}

func runParallel(workers, total int, fn func(ctx context.Context, i int) error) (int, error) {
	if total == 0 {
		return 0, nil
	}
	if workers > total {
		workers = total
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan int, total)
	for i := range total {
		jobs <- i
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		done     int
		firstErr error
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := fn(ctx, i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					return
				}
				mu.Lock()
				done++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return done, firstErr
}
