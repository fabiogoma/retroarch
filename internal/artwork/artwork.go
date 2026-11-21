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
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	ConfigDir  string
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
	if o.ConfigDir != "" {
		mergeCurationMetadata(o.ConfigDir, selections)
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

var badDumpRe = regexp.MustCompile(`(?i)\[b\d|\[h\d|\[t\d`)

// mergeCurationMetadata enriches exported selections with aliases and allow_japanese
// from curation.yaml so newly added aliases take effect without rebuilding exports.
func mergeCurationMetadata(configDir string, selections map[string][]library.Selection) {
	var curated map[string][]library.Entry
	if err := util.ReadYAML(filepath.Join(configDir, "curation.yaml"), &curated); err != nil {
		return
	}
	for console, picks := range selections {
		byTitle := make(map[string]library.Entry, len(curated[console]))
		for _, e := range curated[console] {
			byTitle[e.Title] = e
		}
		for i, p := range picks {
			e, ok := byTitle[p.Title]
			if !ok {
				continue
			}
			if e.AllowJapanese {
				picks[i].AllowJapanese = true
			}
			if len(e.Aliases) == 0 {
				continue
			}
			seen := make(map[string]bool, len(p.Aliases))
			for _, a := range p.Aliases {
				seen[a] = true
			}
			for _, a := range e.Aliases {
				if !seen[a] {
					picks[i].Aliases = append(picks[i].Aliases, a)
					seen[a] = true
				}
			}
		}
	}
}

// boxArtKey relaxes Normalized by dropping "and" so "&"/"and" matches Libretro's "_" and "-".
func boxArtKey(s string) string {
	var words []string
	for _, w := range strings.Fields(library.Normalized(s)) {
		if w != "and" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

func pickBestCandidate(available []string, matchFn func(name string) bool, allowJapanese, allowFallback bool) string {
	var bestName string
	bestScore := -10000
	for _, name := range available {
		if !matchFn(name) {
			continue
		}
		if allowFallback && badDumpRe.MatchString(name) {
			continue
		}
		score := library.RegionScore(name)
		if !allowFallback && (score <= -100 || (score < 0 && !allowJapanese)) {
			continue
		}
		if bestName == "" || score > bestScore || (score == bestScore && name < bestName) {
			bestName = name
			bestScore = score
		}
	}
	return bestName
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

	keys := map[string]bool{library.Normalized(p.Title): true}
	relaxedKeys := map[string]bool{boxArtKey(p.Title): true}
	for _, a := range p.Aliases {
		keys[library.Normalized(a)] = true
		relaxedKeys[boxArtKey(a)] = true
	}

	matchExact := func(name string) bool {
		return keys[library.Normalized(library.TitleOf(name))]
	}
	matchRelaxedFull := func(name string) bool {
		return relaxedKeys[boxArtKey(library.TitleOf(name))]
	}
	// No-Intro dual titles (e.g. "Ys - The Vanished Omens / Y's") are sanitized with " _ ".
	matchRelaxedSplit := func(name string) bool {
		title := library.TitleOf(name)
		if !strings.Contains(title, " _ ") {
			return false
		}
		for _, part := range strings.Split(title, " _ ") {
			if k := boxArtKey(part); k != "" && relaxedKeys[k] {
				return true
			}
		}
		return false
	}

	// 2. Exact normalized title / alias match in preferred regions.
	if m := pickBestCandidate(available, matchExact, p.AllowJapanese, false); m != "" {
		return m
	}
	// 3. Relaxed box-art key match (handles "&"/"and" vs "_"/"-" and dual-title " _ ").
	if m := pickBestCandidate(available, matchRelaxedFull, p.AllowJapanese, false); m != "" {
		return m
	}
	if m := pickBestCandidate(available, matchRelaxedSplit, p.AllowJapanese, false); m != "" {
		return m
	}
	// 4. Last-resort fallback to Proto/Beta/Sample/Japan box art when no retail release exists.
	if m := pickBestCandidate(available, matchExact, true, true); m != "" {
		return m
	}
	if m := pickBestCandidate(available, matchRelaxedFull, true, true); m != "" {
		return m
	}
	return pickBestCandidate(available, matchRelaxedSplit, true, true)
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
	const maxSymlinkHops = 5
	curURL := rawURL
	for hop := 0; hop <= maxSymlinkHops; hop++ {
		data, err := getBytes(ctx, client, curURL)
		if err != nil {
			return err
		}
		if bytes.HasPrefix(data, pngMagic) {
			return writeAtomic(dest, data)
		}
		// raw.githubusercontent.com serves Git symlinks as plain text containing the relative target path.
		nextURL, ok := resolveGitSymlinkURL(curURL, data)
		if !ok || hop == maxSymlinkHops {
			return fmt.Errorf("response is not a valid PNG: %s", curURL)
		}
		curURL = nextURL
	}
	return fmt.Errorf("response is not a valid PNG: %s", rawURL)
}

func resolveGitSymlinkURL(rawURL string, data []byte) (string, bool) {
	if len(data) == 0 || len(data) > 1024 {
		return "", false
	}
	target := string(bytes.TrimRight(data, "\r\n"))
	if target == "" || !utf8.ValidString(target) || strings.ContainsAny(target, "\r\n\x00") {
		return "", false
	}
	if strings.HasPrefix(target, "/") || strings.Contains(target, "://") {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(target), ".png") {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	resolved := path.Clean(path.Join(path.Dir(u.Path), target))
	if !strings.HasPrefix(resolved, "/") || resolved == u.Path {
		return "", false
	}
	u.Path = resolved
	u.RawPath = ""
	return u.String(), true
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
