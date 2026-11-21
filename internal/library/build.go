package library

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

// Selection is one exported game, as written to selection.json.
type Selection struct {
	Rank int `json:"rank"`
	Entry
	File          string `json:"file"`
	Source        string `json:"source"`
	ArchiveMember string `json:"archive_member"`
	LanguageNote  string `json:"language_note"`
	ROMSHA256     string `json:"rom_sha256"`
	ExportSHA256  string `json:"export_sha256"`
	Bytes         int64  `json:"bytes"`
}

// ConsoleReport is the per-console section of summary.json.
type ConsoleReport struct {
	Console         string `json:"console"`
	CatalogVariants int    `json:"catalog_variants"`
	UniqueTitles    int    `json:"unique_titles"`
	Selected        int    `json:"selected"`
	Target          int    `json:"target"`
	Bytes           int64  `json:"bytes"`
}

// MissingPick records a curated game that had no matching local ROM.
type MissingPick struct {
	Console string `json:"console"`
	Title   string `json:"title"`
	Reason  string `json:"reason"`
}

// Summary is written to <export>/summary.json.
type Summary struct {
	GeneratedUTC   string          `json:"generated_utc"`
	Consoles       []ConsoleReport `json:"consoles"`
	MissingCurated []MissingPick   `json:"missing_curated"`
	Issues         []ScanIssue     `json:"issues"`
}

// copyFile is replaceable so tests can simulate a failing disk.
var copyFile = func(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

var badFileChars = regexp.MustCompile(`[/\\:*?"<>|]`)

func better(a, b Game) bool {
	if a.RegionScore != b.RegionScore {
		return a.RegionScore > b.RegionScore
	}
	if (a.Member == "") != (b.Member == "") {
		return a.Member == ""
	}
	return a.Path > b.Path
}

// BuildOptions controls building one or more Top-N exports.
type BuildOptions struct {
	DataDir    string
	StateDir   string
	ROMsDir    string
	Counts     []int
	Unzip      bool
	FromCache  bool
	DeviceHome string
}

// Build scans local ROMs (or loads scan-cache.json) and builds each requested Top-N export
// folder along with its Apple TV RetroArch playlists.
func Build(o BuildOptions) error {
	if len(o.Counts) == 0 {
		return fmt.Errorf("at least one top count is required")
	}
	for _, c := range o.Counts {
		if c < 1 {
			return fmt.Errorf("top count must be positive: %d", c)
		}
	}
	if o.DeviceHome == "" {
		o.DeviceHome = "~"
	}
	lib, err := Load(o.DataDir)
	if err != nil {
		return err
	}
	if err := lib.LoadCuration(o.DataDir); err != nil {
		return err
	}
	state := o.StateDir
	if state == "" {
		state = filepath.Join(o.DataDir, "state")
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		return err
	}
	unlock, err := util.Lock(filepath.Join(state, "run.lock"))
	if err != nil {
		return err
	}
	defer unlock()

	cachePath := filepath.Join(state, "scan-cache.json")
	var scan *ScanResult
	if o.FromCache {
		scan, err = LoadScanCache(cachePath)
		if err != nil {
			return fmt.Errorf("load scan cache: %w (run without --from-cache to scan ROMs/)", err)
		}
	} else {
		scan, err = lib.Scan(o.ROMsDir)
		if err != nil {
			return err
		}
		if err := SaveScanCache(cachePath, scan); err != nil {
			return err
		}
	}

	for _, count := range o.Counts {
		reports, err := lib.Export(o.DataDir, o.ROMsDir, state, scan, count, o.Unzip, o.DeviceHome)
		if err != nil {
			return err
		}
		fmt.Printf("\nBuilt %s%d:\n", ExportPrefix, count)
		for _, r := range reports {
			fmt.Printf("  %-14s %2d / %d games (%d MB)\n", r.Console, r.Selected, r.Target, r.Bytes/(1024*1024))
		}
	}
	return nil
}

// Export builds <roms>/<ExportPrefix><count> with ROMs, manifests, and Apple TV playlists.
func (l *Library) Export(dataDir, roms, state string, scan *ScanResult, count int, unzip bool, deviceHome string) ([]ConsoleReport, error) {
	target := filepath.Join(roms, fmt.Sprintf("%s%d", ExportPrefix, count))
	stage, err := os.MkdirTemp(state, "export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	byConsole := map[string][]Game{}
	set := map[string]bool{}
	for _, g := range scan.Games {
		byConsole[g.Console] = append(byConsole[g.Console], g)
		set[g.Console] = true
	}
	for c := range l.Curated {
		set[c] = true
	}
	var consoles []string
	for c := range set {
		consoles = append(consoles, c)
	}
	sort.Strings(consoles)

	reports := []ConsoleReport{}
	misses := []MissingPick{}
	selectionsByConsole := map[string][]Selection{}

	for _, console := range consoles {
		folder := filepath.Join(stage, console)
		if err := os.Mkdir(folder, 0o755); err != nil {
			return nil, err
		}
		games := byConsole[console]
		var ranked []rankedPick
		used := map[string]bool{}
		for _, entry := range l.Curated[console] {
			keys := map[string]bool{Normalized(entry.Title): true}
			for _, a := range entry.Aliases {
				keys[Normalized(a)] = true
			}
			var best *Game
			for i := range games {
				g := games[i]
				ok := keys[g.TitleKey] && (g.RegionScore >= 0 || entry.AllowJapanese && g.RegionScore > -100) && !used[g.SHA256]
				if ok && (best == nil || better(g, *best)) {
					best = &games[i]
				}
			}
			if best == nil {
				misses = append(misses, MissingPick{console, entry.Title, "No matching preferred-language ROM"})
				continue
			}
			if len(ranked) >= count {
				continue
			}
			ranked = append(ranked, rankedPick{entry, *best})
			used[best.SHA256] = true
		}

		manifest := make([]Selection, len(ranked))
		if err := exportRankedParallel(ranked, folder, unzip, manifest); err != nil {
			return nil, err
		}
		selectionsByConsole[console] = manifest
		if err := util.WriteJSON(filepath.Join(folder, "selection.json"), manifest); err != nil {
			return nil, err
		}
		lines := []string{
			fmt.Sprintf("# %s: %d / %d picks", console, len(manifest), count), "",
			"| # | Game | Play mode | Notes |", "|---|---|---|---|",
		}
		var totalBytes int64
		for _, m := range manifest {
			lines = append(lines, fmt.Sprintf("| %d | %s | %s | %s |", m.Rank, m.Title, m.PlayMode, m.FamilyNotes))
			totalBytes += m.Bytes
		}
		if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("TOP%d.md", count)), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			return nil, err
		}
		keys := map[string]bool{}
		for _, g := range games {
			keys[g.TitleKey] = true
		}
		reports = append(reports, ConsoleReport{console, len(games), len(keys), len(manifest), count, totalBytes})
	}

	if err := writePlaylists(dataDir, filepath.Join(stage, "playlists"), selectionsByConsole, deviceHome); err != nil {
		return nil, err
	}

	issues := scan.Issues
	if issues == nil {
		issues = []ScanIssue{}
	}
	summary := Summary{
		GeneratedUTC:   time.Now().UTC().Format(time.RFC3339),
		Consoles:       reports,
		MissingCurated: misses,
		Issues:         issues,
	}
	if err := util.WriteJSON(filepath.Join(stage, "summary.json"), summary); err != nil {
		return nil, err
	}

	backup := filepath.Join(state, "previous-export")
	if err := os.RemoveAll(backup); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(target)
	hadTarget := statErr == nil
	if hadTarget {
		if err := os.Rename(target, backup); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(stage, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return nil, err
	}
	return reports, nil
}

type playlistItem struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	CorePath string `json:"core_path"`
	CoreName string `json:"core_name"`
	CRC32    string `json:"crc32"`
	DBName   string `json:"db_name"`
}

type playlist struct {
	Version         string         `json:"version"`
	DefaultCorePath string         `json:"default_core_path"`
	DefaultCoreName string         `json:"default_core_name"`
	Items           []playlistItem `json:"items"`
}

func writePlaylists(dataDir, outDir string, selections map[string][]Selection, deviceHome string) error {
	home := deviceHome
	if home != "~" {
		if !strings.HasPrefix(home, "/") {
			return fmt.Errorf("--device-home must be ~ or an absolute path")
		}
		home = path.Clean(home)
	}
	for _, part := range strings.Split(deviceHome, "/") {
		if part == ".." {
			return fmt.Errorf("--device-home must be ~ or an absolute path")
		}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	var cores []struct {
		System   string `yaml:"system"`
		CorePath string `yaml:"core_path"`
		CoreName string `yaml:"core_name"`
	}
	if err := util.ReadYAML(filepath.Join(dataDir, "apple_tv_cores.yaml"), &cores); err != nil {
		return err
	}
	for _, c := range Consoles {
		entries, ok := selections[c.Folder]
		if !ok {
			continue
		}
		corePath, coreName := "DETECT", "DETECT"
		for _, cr := range cores {
			if cr.System == c.System {
				if cr.CorePath != "" {
					corePath = cr.CorePath
				}
				if cr.CoreName != "" {
					coreName = cr.CoreName
				}
			}
		}
		pl := playlist{Version: "1.5", DefaultCorePath: corePath, DefaultCoreName: coreName, Items: []playlistItem{}}
		for _, e := range entries {
			item := path.Join(home, "Library/Caches/RetroArch/downloads/ROMS", c.Folder, e.File)
			if strings.HasSuffix(strings.ToLower(e.File), ".zip") && e.ArchiveMember != "" {
				item += "#" + e.ArchiveMember
			}
			pl.Items = append(pl.Items, playlistItem{item, e.Title, corePath, coreName, "DETECT", c.System + ".lpl"})
		}
		data, err := util.MarshalIndent(pl)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, c.System+".lpl"), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

type rankedPick struct {
	entry Entry
	game  Game
}

func workerCount(n int) int {
	w := runtime.GOMAXPROCS(0) * 2
	if w < 4 {
		w = 4
	}
	if w > 8 {
		w = 8
	}
	if n < w {
		w = n
	}
	return w
}

func exportRankedParallel(ranked []rankedPick, folder string, unzip bool, manifest []Selection) error {
	if len(ranked) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan int, len(ranked))
	for i := range ranked {
		jobs <- i
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	for range workerCount(len(ranked)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				p := ranked[i]
				g := p.game
				suffix := strings.ToLower(filepath.Ext(g.Path))
				if unzip && g.Member != "" {
					suffix = strings.ToLower(filepath.Ext(g.Member))
				}
				filename := badFileChars.ReplaceAllString(p.entry.Title, "-") + suffix
				out := filepath.Join(folder, filename)
				var err error
				if unzip && g.Member != "" {
					err = extractMember(g.Path, g.Member, out)
				} else {
					err = copyFile(g.Path, out)
				}
				if err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				sha, size, err := hashFile(out)
				if err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				note := "Preferred USA/World/Europe variant; language inferred from filename"
				if g.RegionScore < 0 {
					note = "Japanese source; menus may need help"
				}
				manifest[i] = Selection{
					Rank: i + 1, Entry: p.entry, File: filename, Source: g.Path, ArchiveMember: g.Member,
					LanguageNote: note, ROMSHA256: g.SHA256, ExportSHA256: sha, Bytes: size,
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func extractMember(zipPath, member, out string) error {
	z, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != member {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		w, err := os.Create(out)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}
	return errors.New("member not found: " + member)
}

// ParseCounts parses a comma-separated list of counts like "30,40,50" or "all".
func ParseCounts(spec string) ([]int, error) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" || spec == "all" {
		return []int{30, 40, 50}, nil
	}
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(part, "%d", &n); err != nil || n < 1 {
			return nil, fmt.Errorf("invalid top count %q (expected positive integers like 30,40,50)", part)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid top counts in %q", spec)
	}
	return out, nil
}

// LoadExportSelections reads selection.json for every console in an export folder.
func LoadExportSelections(exportDir string) (map[string][]Selection, error) {
	var summary Summary
	if err := util.ReadJSON(filepath.Join(exportDir, "summary.json"), &summary); err != nil {
		return nil, err
	}
	out := map[string][]Selection{}
	for _, c := range summary.Consoles {
		if !util.SafeName(c.Console) {
			return nil, fmt.Errorf("invalid console name in summary: %q", c.Console)
		}
		var entries []Selection
		if err := util.ReadJSON(filepath.Join(exportDir, c.Console, "selection.json"), &entries); err != nil {
			return nil, err
		}
		out[c.Console] = entries
	}
	return out, nil
}
