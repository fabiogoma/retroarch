// Package library builds the offline ROM catalog and the curated Apple TV export.
package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var archiveExts = map[string]bool{".zip": true, ".7z": true, ".rar": true, ".tar": true, ".gz": true, ".bz2": true, ".xz": true, ".tgz": true}

const maxExtract = 32 << 30

// Profile describes how a console's files are recognised.
type Profile struct {
	Extensions    []string `json:"extensions"`
	FolderAliases []string `json:"folder_aliases"`
}

// Library holds the static configuration for a run.
type Library struct {
	Profiles map[string]Profile
	romExts  map[string]bool
	Curated  map[string][]Entry
}

// Entry is one reviewed pick from curation.json.
type Entry struct {
	Title         string   `json:"title"`
	Aliases       []string `json:"aliases"`
	PlayMode      string   `json:"play_mode"`
	Genre         string   `json:"genre"`
	Reason        string   `json:"reason"`
	FamilyNotes   string   `json:"family_notes"`
	Popularity    string   `json:"popularity"`
	Sources       []any    `json:"sources"`
	AllowJapanese bool     `json:"allow_japanese,omitempty"`
	Mickey        bool     `json:"mickey,omitempty"`
}

// Load reads consoles.json and curation.json from the data directory.
func Load(dataDir string) (*Library, error) {
	l := &Library{romExts: map[string]bool{}}
	if err := readJSON(filepath.Join(dataDir, "consoles.json"), &l.Profiles); err != nil {
		return nil, err
	}
	for _, p := range l.Profiles {
		for _, e := range p.Extensions {
			l.romExts[e] = true
		}
	}
	return l, nil
}

// LoadCuration reads curation.json.
func (l *Library) LoadCuration(dataDir string) error {
	return readJSON(filepath.Join(dataDir, "curation.json"), &l.Curated)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

var (
	parenRe    = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)
	possessRe  = regexp.MustCompile(`\b(\w+)\s+s\b`)
	nonAlnumRe = regexp.MustCompile(`[^a-z0-9 ]`)
	spaceRe    = regexp.MustCompile(`\s+`)
)

// Normalized builds the comparison key used to match titles across regions.
func Normalized(s string) string {
	s = parenRe.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if r < 128 {
			b.WriteRune(r)
		}
	}
	s = strings.ToLower(b.String())
	s = strings.ReplaceAll(s, "megaman", "mega man")
	s = strings.ReplaceAll(s, " & ", " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = possessRe.ReplaceAllString(s, "${1}s")
	s = nonAlnumRe.ReplaceAllString(s, " ")
	var words []string
	for _, w := range strings.Fields(s) {
		if w != "the" && w != "a" && w != "an" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// TitleOf strips the extension and bracketed tags from a file name.
func TitleOf(name string) string {
	base := filepath.Base(name)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return strings.TrimSpace(spaceRe.ReplaceAllString(parenRe.ReplaceAllString(stem, ""), " "))
}

var (
	usaRe     = regexp.MustCompile(`(?i)\(USA|\(U\)|Japan, USA`)
	worldRe   = regexp.MustCompile(`(?i)\(World|\(W\)`)
	europeRe  = regexp.MustCompile(`(?i)Europe|\(E\)`)
	englishRe = regexp.MustCompile(`\(En(?:[,)]|\))`)
	japanRe   = regexp.MustCompile(`Japan|\(J\)`)
	notJapan  = regexp.MustCompile(`USA|World|En[,)]`)
	badRe     = regexp.MustCompile(`(?i)Beta|Proto|Sample|\(Unl\)|\[b\d|\[h\d|\[t\d`)
)

// RegionScore ranks releases; negative means unsuitable, -100 or less is never used.
func RegionScore(name string) int {
	score := 0
	switch {
	case usaRe.MatchString(name):
		score += 30
	case worldRe.MatchString(name):
		score += 25
	case europeRe.MatchString(name):
		score += 20
	}
	if englishRe.MatchString(name) {
		score += 5
	}
	if japanRe.MatchString(name) && !notJapan.MatchString(name) {
		score -= 30
	}
	if badRe.MatchString(name) {
		score -= 100
	}
	return score
}

func (l *Library) consoleFor(folder, ext string) string {
	names := make([]string, 0, len(l.Profiles))
	for n := range l.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	lf := strings.ToLower(folder)
	for _, n := range names {
		for _, a := range l.Profiles[n].FolderAliases {
			if a == lf {
				return n
			}
		}
	}
	var matches []string
	for _, n := range names {
		for _, e := range l.Profiles[n].Extensions {
			if e == ext {
				matches = append(matches, n)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return folder
}
