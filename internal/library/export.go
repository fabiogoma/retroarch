package library

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

type game struct {
	ID          int64
	Console     string
	Title       string
	TitleKey    string
	Path        string
	Member      string
	SHA256      string
	Bytes       int64
	RegionScore int
}

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

type missing struct {
	Console string `json:"console"`
	Title   string `json:"title"`
	Reason  string `json:"reason"`
}

type issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Summary is summary.json.
type Summary struct {
	GeneratedUTC       string          `json:"generated_utc"`
	Consoles           []ConsoleReport `json:"consoles"`
	MissingCurated     []missing       `json:"missing_curated"`
	Issues             []issue         `json:"issues"`
	IncompleteFiles    []string        `json:"incomplete_files"`
	DownloadLog        string          `json:"download_log"`
	UnreviewedConsoles []string        `json:"unreviewed_consoles"`
	SelectionPolicy    string          `json:"selection_policy"`
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

func loadGames(db DB, console string) ([]game, error) {
	rows, err := db.Query("SELECT id,console,title,title_key,path,member,sha256,bytes,region_score FROM games WHERE console=?", console)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var games []game
	for rows.Next() {
		var g game
		if err := rows.Scan(&g.ID, &g.Console, &g.Title, &g.TitleKey, &g.Path, &g.Member, &g.SHA256, &g.Bytes, &g.RegionScore); err != nil {
			return nil, err
		}
		games = append(games, g)
	}
	return games, rows.Err()
}

func better(a, b game) bool {
	if a.RegionScore != b.RegionScore {
		return a.RegionScore > b.RegionScore
	}
	if (a.Member == "") != (b.Member == "") {
		return a.Member == ""
	}
	return a.Path > b.Path
}

func writeReadme(stage string, count int) error {
	text := fmt.Sprintf("# Apple TV upload set\n\nTo transfer missing ROMs, run `retroarch sync --export ROMs/%[2]s%[1]d` from the project root. For manual uploads, transfer only the ROM files inside each console folder using RetroArch’s web file server. Keep this folder structure. Do not upload the catalog, original collection bundles or unfinished downloads. On Apple TV, use Import Content → Manual Scan, select the console folder and system, and enable Scan Inside Archives for ZIPs. Archive content is one game per ZIP. If a core cannot load a ZIP, rerun with --unzip-export.\n\nRecommended cores: NES: FCEUmm or Nestopia; SNES: Snes9x; Genesis and Master System: Genesis Plus GX; PC Engine: Beetle PCE FAST; N64: an available N64 core. Core availability and N64 performance depend on the Apple TV build/device; no device play test was performed.\n\nTOP%[1]d.md and selection.json describe every pick. Missing games are in summary.json; no padding with unsuitable or unreviewed games. At age two, treat these as parent-led watching/brief controller play; colorful games can still be difficult. Preview fantasy combat and spooky scenes before family play.\n", count, ExportPrefix)
	return os.WriteFile(filepath.Join(stage, "README.md"), []byte(text), 0o644)
}

// Export copies the curated picks into roms/AppleTV-Top<count>, replacing the
// previous export only after the new one is complete.
func (l *Library) Export(roms, state string, db DB, count int, unzip bool) (reports []ConsoleReport, err error) {
	target := filepath.Join(roms, fmt.Sprintf("%s%d", ExportPrefix, count))
	stage, err := os.MkdirTemp(state, "export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	set := map[string]bool{}
	rows, err := db.Query("SELECT DISTINCT console FROM games")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, err
		}
		set[c] = true
	}
	rows.Close()
	for c := range l.Curated {
		set[c] = true
	}
	var consoles []string
	for c := range set {
		consoles = append(consoles, c)
	}
	sort.Strings(consoles)

	reports = []ConsoleReport{}
	misses := []missing{}
	for _, console := range consoles {
		folder := filepath.Join(stage, console)
		if err := os.Mkdir(folder, 0o755); err != nil {
			return nil, err
		}
		games, err := loadGames(db, console)
		if err != nil {
			return nil, err
		}
		var ranked []rankedPick
		used := map[string]bool{}
		for _, entry := range l.Curated[console] {
			keys := map[string]bool{Normalized(entry.Title): true}
			for _, a := range entry.Aliases {
				keys[Normalized(a)] = true
			}
			var best *game
			for i := range games {
				g := games[i]
				ok := keys[g.TitleKey] && (g.RegionScore >= 0 || entry.AllowJapanese && g.RegionScore > -100) && !used[g.SHA256]
				if ok && (best == nil || better(g, *best)) {
					best = &games[i]
				}
			}
			if best == nil {
				misses = append(misses, missing{console, entry.Title, "No matching complete preferred-language ROM"})
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
		for i, p := range ranked {
			item := manifest[i]
			details, _ := json.Marshal(item)
			if _, err := db.Exec("INSERT INTO selections VALUES (?,?,?,?,?)", console, item.Rank, p.game.ID, p.entry.Title, string(details)); err != nil {
				return nil, err
			}
		}
		if err := util.WriteJSON(filepath.Join(folder, "selection.json"), manifest); err != nil {
			return nil, err
		}
		lines := []string{fmt.Sprintf("# %s: %d / %d available picks", console, len(manifest), count), "",
			"Order is a personal recommendation, not a sales chart. Notes are parental guidance, not age ratings.", "",
			"| # | Game | Play mode | Family notes | Why selected | Popularity evidence |", "|---|---|---|---|---|---|"}
		var bytes int64
		for _, m := range manifest {
			lines = append(lines, fmt.Sprintf("| %d | %s | %s | %s | %s | %s |", m.Rank, m.Title, m.PlayMode, m.FamilyNotes, m.Reason, m.Popularity))
			bytes += m.Bytes
		}
		if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("TOP%d.md", count)), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			return nil, err
		}
		keys := map[string]bool{}
		for _, g := range games {
			keys[g.TitleKey] = true
		}
		reports = append(reports, ConsoleReport{console, len(games), len(keys), len(manifest), count, bytes})
	}

	summary := Summary{
		GeneratedUTC: time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00"), Consoles: reports, MissingCurated: misses,
		Issues: []issue{}, IncompleteFiles: []string{}, DownloadLog: filepath.Join(state, "master_system_downloads.json"),
		UnreviewedConsoles: []string{},
		SelectionPolicy:    "Editorial order combining published popularity evidence with family/taste fit; not a sales chart. New consoles need a reviewed curation.json list.",
	}
	for _, c := range consoles {
		if _, ok := l.Curated[c]; !ok {
			summary.UnreviewedConsoles = append(summary.UnreviewedConsoles, c)
		}
	}
	irows, err := db.Query("SELECT path,message FROM issues")
	if err != nil {
		return nil, err
	}
	for irows.Next() {
		var i issue
		if err := irows.Scan(&i.Path, &i.Message); err != nil {
			irows.Close()
			return nil, err
		}
		summary.Issues = append(summary.Issues, i)
	}
	irows.Close()
	frows, err := db.Query("SELECT path FROM files WHERE status='incomplete'")
	if err != nil {
		return nil, err
	}
	for frows.Next() {
		var p string
		if err := frows.Scan(&p); err != nil {
			frows.Close()
			return nil, err
		}
		summary.IncompleteFiles = append(summary.IncompleteFiles, p)
	}
	frows.Close()
	if err := util.WriteJSON(filepath.Join(stage, "summary.json"), summary); err != nil {
		return nil, err
	}
	if err := writeReadme(stage, count); err != nil {
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

type rankedPick struct {
	entry Entry
	game  game
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
	workers := workerCount(len(ranked))
	for range workers {
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
