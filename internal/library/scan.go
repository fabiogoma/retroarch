package library

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// ScanIssue records an unreadable or invalid file encountered during scanning.
type ScanIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ScanResult holds all discovered games and any issues from a scan.
type ScanResult struct {
	Games  []Game      `json:"games"`
	Issues []ScanIssue `json:"issues"`
}

// ExportPrefix names export folders (<prefix><count>); set from configuration.
var ExportPrefix = "AppleTV-Top"

func isExportDir(name string) bool {
	return regexp.MustCompile("^" + regexp.QuoteMeta(ExportPrefix) + "[0-9]+$").MatchString(name)
}

// SafeMember validates an archive entry name as a safe relative path.
func SafeMember(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	parts := strings.Split(n, "/")
	if n == "" || strings.HasPrefix(n, "/") || parts[0] == "" || strings.Contains(parts[0], ":") {
		return "", errors.New("Unsafe member: " + name)
	}
	clean := parts[:0]
	for _, p := range parts {
		if p == ".." {
			return "", errors.New("Unsafe member: " + name)
		}
		if p == "" || p == "." {
			continue
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", errors.New("Unsafe member: " + name)
	}
	return filepath.Join(clean...), nil
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

func inspectSingleROMZip(path string, romExts map[string]bool) (member, sha string, size int64, err error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return "", "", 0, err
	}
	defer z.Close()

	var target *zip.File
	for _, f := range z.File {
		mode := f.Mode()
		if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if !mode.IsRegular() {
			return "", "", 0, errors.New("Archive contains a link or special entry: " + f.Name)
		}
		if _, err := SafeMember(f.Name); err != nil {
			return "", "", 0, err
		}
		if target != nil {
			return "", "", 0, errors.New("Multi-file ZIP bundle is not supported; extract ROMs into the console folder")
		}
		target = f
	}
	if target == nil {
		return "", "", 0, errors.New("Empty ZIP archive")
	}
	if !romExts[strings.ToLower(filepath.Ext(target.Name))] {
		return "", "", 0, fmt.Errorf("ZIP member %q is not a recognized ROM", target.Name)
	}
	r, err := target.Open()
	if err != nil {
		return "", "", 0, err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", "", 0, err
	}
	return target.Name, hex.EncodeToString(h.Sum(nil)), n, nil
}

type scanJob struct {
	folder string
	path   string
	isZip  bool
}

type scanOutcome struct {
	game  Game
	issue *ScanIssue
}

// Scan finds every ROM directly inside each console folder under roms,
// skipping export folders and hidden dirs. Single-ROM .zip files are read in place.
func (l *Library) Scan(roms string) (*ScanResult, error) {
	folders, err := os.ReadDir(roms)
	if err != nil {
		return nil, err
	}
	var jobs []scanJob
	for _, folder := range folders {
		name := folder.Name()
		if !folder.IsDir() || strings.HasPrefix(name, ".") || name == "config" || name == "state" || name == "artwork" || isExportDir(name) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(roms, name))
		if err != nil {
			return nil, err
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, f := range names {
			ext := strings.ToLower(filepath.Ext(f))
			if ext == ".zip" {
				jobs = append(jobs, scanJob{folder: name, path: filepath.Join(roms, name, f), isZip: true})
			} else if l.romExts[ext] {
				jobs = append(jobs, scanJob{folder: name, path: filepath.Join(roms, name, f)})
			}
		}
	}

	res := &ScanResult{Games: []Game{}, Issues: []ScanIssue{}}
	if len(jobs) == 0 {
		return res, nil
	}

	outcomes := make([]scanOutcome, len(jobs))
	ch := make(chan int, len(jobs))
	for i := range jobs {
		ch <- i
	}
	close(ch)

	var wg sync.WaitGroup
	for range workerCount(len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				j := jobs[i]
				var member, sha string
				var size int64
				var err error
				if j.isZip {
					member, sha, size, err = inspectSingleROMZip(j.path, l.romExts)
				} else {
					sha, size, err = hashFile(j.path)
				}
				if err != nil {
					outcomes[i] = scanOutcome{issue: &ScanIssue{Path: j.path, Message: err.Error()}}
					continue
				}
				displayName := member
				if displayName == "" {
					displayName = filepath.Base(j.path)
				}
				title := TitleOf(displayName)
				outcomes[i] = scanOutcome{game: Game{
					Console:     l.consoleFor(j.folder, strings.ToLower(filepath.Ext(displayName))),
					Title:       title,
					TitleKey:    Normalized(title),
					Path:        j.path,
					Member:      member,
					SHA256:      sha,
					Bytes:       size,
					RegionScore: RegionScore(displayName),
				}}
			}
		}()
	}
	wg.Wait()

	for _, o := range outcomes {
		if o.issue != nil {
			fmt.Println("ISSUE", o.issue.Path, o.issue.Message)
			res.Issues = append(res.Issues, *o.issue)
			continue
		}
		res.Games = append(res.Games, o.game)
	}
	return res, nil
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
