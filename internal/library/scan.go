package library

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/fabiogoma/retroarch/internal/util"
)

// Game is one discovered ROM file (or single-ROM ZIP member).
type Game struct {
	Console     string `json:"console"`
	Title       string `json:"title"`
	TitleKey    string `json:"title_key"`
	Path        string `json:"path"`
	Member      string `json:"member,omitempty"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	RegionScore int    `json:"region_score"`
}

// ScanIssue records an unreadable or invalid archive encountered during scanning.
type ScanIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ScanResult holds all discovered games and any issues from a scan.
type ScanResult struct {
	Games  []Game      `json:"games"`
	Issues []ScanIssue `json:"issues"`
}

type archiveMeta struct {
	Kind    string   `json:"kind"`
	Members []Member `json:"members"`
}

type candidate struct {
	path   string
	member string
}

// ExportPrefix names export folders (<prefix><count>); set from configuration.
var ExportPrefix = "AppleTV-Top"

func isExportDir(name string) bool {
	return regexp.MustCompile("^" + regexp.QuoteMeta(ExportPrefix) + "[0-9]+$").MatchString(name)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func (l *Library) prepareArchive(path, state string, depth int) ([]candidate, error) {
	if depth > 8 {
		return nil, errors.New("Nested archive depth exceeds 8")
	}
	sha, _, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(state, "extracted", sha)
	complete := filepath.Join(dest, ".complete.json")

	var meta archiveMeta
	haveMeta := util.ReadJSON(complete, &meta) == nil
	if !haveMeta {
		listed, err := ListArchive(path)
		if err != nil {
			return nil, err
		}
		var total int64
		for _, m := range listed {
			if _, err := SafeMember(m.Name); err != nil {
				return nil, err
			}
			total += m.Size
		}
		if total > maxExtract {
			return nil, errors.New("Archive exceeds 32 GiB extraction limit")
		}
		kind := "extract"
		if strings.EqualFold(filepath.Ext(path), ".zip") && len(listed) == 1 &&
			l.romExts[strings.ToLower(filepath.Ext(listed[0].Name))] {
			kind = "single_zip"
		}
		meta = archiveMeta{Kind: kind, Members: listed}
	}
	if meta.Kind == "single_zip" {
		return []candidate{{path, meta.Members[0].Name}}, nil
	}
	if !haveMeta {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		temp, err := os.MkdirTemp(filepath.Dir(dest), "unpack-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(temp)
		fmt.Println("Extracting", path)
		if err := extractArchive(path, temp, maxExtract); err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(temp, ".complete.json"), raw, 0o644); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(dest); err != nil {
			return nil, err
		}
		if err := os.Rename(temp, dest); err != nil {
			return nil, err
		}
	}
	var files []string
	err = filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var result []candidate
	for _, p := range files {
		ext := strings.ToLower(filepath.Ext(p))
		if archiveExts[ext] {
			sub, err := l.prepareArchive(p, state, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, sub...)
		} else if l.romExts[ext] {
			result = append(result, candidate{p, ""})
		}
	}
	return result, nil
}

func hashZipMember(path, member string) (string, int64, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return "", 0, err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != member {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return "", 0, err
		}
		defer r.Close()
		h := sha256.New()
		n, err := io.Copy(h, r)
		return hex.EncodeToString(h.Sum(nil)), n, err
	}
	return "", 0, fmt.Errorf("member %q not found in %s", member, path)
}

// Scan recursively finds every ROM under roms, skipping export folders and hidden dirs.
func (l *Library) Scan(roms, state string) (*ScanResult, error) {
	folders, err := os.ReadDir(roms)
	if err != nil {
		return nil, err
	}
	res := &ScanResult{Games: []Game{}, Issues: []ScanIssue{}}
	seen := map[string]bool{}
	for _, folder := range folders {
		name := folder.Name()
		if !folder.IsDir() || strings.HasPrefix(name, ".") || name == "rom_library" || name == "artwork" || isExportDir(name) {
			continue
		}
		var paths []string
		err := filepath.WalkDir(filepath.Join(roms, name), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				paths = append(paths, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(paths)
		for _, p := range paths {
			if err := l.scanFile(p, name, state, res, seen); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}

func (l *Library) scanFile(path, folder, state string, res *ScanResult, seen map[string]bool) error {
	ext := strings.ToLower(filepath.Ext(path))
	kind := "auxiliary"
	if archiveExts[ext] {
		kind = "archive"
	} else if l.romExts[ext] {
		kind = "rom"
	}
	if kind == "auxiliary" {
		return nil
	}
	addIssue := func(err error) {
		fmt.Println("ISSUE", path, err)
		res.Issues = append(res.Issues, ScanIssue{Path: path, Message: err.Error()})
	}
	cands := []candidate{{path, ""}}
	if kind == "archive" {
		var err error
		if cands, err = l.prepareArchive(path, state, 0); err != nil {
			addIssue(err)
			return nil
		}
	}
	type uniqueCand struct {
		c       candidate
		name    string
		console string
	}
	var unique []uniqueCand
	for _, c := range cands {
		name := c.member
		if name == "" {
			name = filepath.Base(c.path)
		}
		console := l.consoleFor(folder, strings.ToLower(filepath.Ext(name)))
		id := console + "\x00" + c.path + "\x00" + c.member
		if seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, uniqueCand{c: c, name: name, console: console})
	}
	if len(unique) == 0 {
		return nil
	}
	type hashedCand struct {
		sha  string
		size int64
		err  error
	}
	results := make([]hashedCand, len(unique))
	if len(unique) == 1 {
		u := unique[0]
		if u.c.member != "" {
			results[0].sha, results[0].size, results[0].err = hashZipMember(u.c.path, u.c.member)
		} else {
			results[0].sha, results[0].size, results[0].err = hashFile(u.c.path)
		}
	} else {
		jobs := make(chan int, len(unique))
		for i := range unique {
			jobs <- i
		}
		close(jobs)
		var wg sync.WaitGroup
		for range workerCount(len(unique)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					u := unique[i]
					if u.c.member != "" {
						results[i].sha, results[i].size, results[i].err = hashZipMember(u.c.path, u.c.member)
					} else {
						results[i].sha, results[i].size, results[i].err = hashFile(u.c.path)
					}
				}
			}()
		}
		wg.Wait()
	}
	for i, u := range unique {
		if results[i].err != nil {
			addIssue(results[i].err)
			continue
		}
		title := TitleOf(u.name)
		res.Games = append(res.Games, Game{
			Console:     u.console,
			Title:       title,
			TitleKey:    Normalized(title),
			Path:        u.c.path,
			Member:      u.c.member,
			SHA256:      results[i].sha,
			Bytes:       results[i].size,
			RegionScore: RegionScore(u.name),
		})
	}
	return nil
}

// SaveScanCache writes the scan result to a JSON cache file.
func SaveScanCache(path string, scan *ScanResult) error {
	return util.WriteJSON(path, scan)
}

// LoadScanCache reads a previously saved scan cache file.
func LoadScanCache(path string) (*ScanResult, error) {
	var scan ScanResult
	if err := util.ReadJSON(path, &scan); err != nil {
		return nil, err
	}
	return &scan, nil
}
