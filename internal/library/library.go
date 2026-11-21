// Package library builds the offline ROM catalog and the curated Apple TV export.
package library

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/fabiogoma/retroarch/internal/util"
)

// ConsoleInfo describes one supported Apple TV console.
type ConsoleInfo struct {
	Slug         string
	Folder       string
	Core         string
	System       string
	LibretroRepo string
	DPadOnly     bool
}

// Consoles is the single source of truth for every supported console.
var Consoles = []ConsoleInfo{
	{Slug: "nes", Folder: "nes", Core: "Nestopia", System: "Nintendo - Nintendo Entertainment System", LibretroRepo: "Nintendo_-_Nintendo_Entertainment_System", DPadOnly: true},
	{Slug: "snes", Folder: "snes", Core: "Snes9x", System: "Nintendo - Super Nintendo Entertainment System", LibretroRepo: "Nintendo_-_Super_Nintendo_Entertainment_System", DPadOnly: true},
	{Slug: "genesis", Folder: "genesis", Core: "Genesis Plus GX", System: "Sega - Mega Drive - Genesis", LibretroRepo: "Sega_-_Mega_Drive_-_Genesis", DPadOnly: true},
	{Slug: "master-system", Folder: "master system", Core: "Genesis Plus GX", System: "Sega - Master System - Mark III", LibretroRepo: "Sega_-_Master_System_-_Mark_III", DPadOnly: true},
	{Slug: "pc-engine", Folder: "PC Engine", Core: "Beetle PCE Fast", System: "NEC - PC Engine - TurboGrafx 16", LibretroRepo: "NEC_-_PC_Engine_-_TurboGrafx_16", DPadOnly: true},
	{Slug: "n64", Folder: "n64", Core: "Mupen64Plus-Next", System: "Nintendo - Nintendo 64", LibretroRepo: "Nintendo_-_Nintendo_64", DPadOnly: false},
}

// ConsoleByFolder looks up console metadata by its ROM folder name.
func ConsoleByFolder(folder string) (ConsoleInfo, bool) {
	for _, c := range Consoles {
		if c.Folder == folder {
			return c, true
		}
	}
	return ConsoleInfo{}, false
}

// SanitizeThumbnailName matches RetroArch's thumbnail filename sanitization rules.
func SanitizeThumbnailName(label string) string {
	var b strings.Builder
	b.Grow(len(label))
	for _, r := range label {
		switch r {
		case '&', '*', '/', ':', '`', '<', '>', '?', '\\', '|':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Profile describes how a console's files are recognised.
type Profile struct {
	Extensions    []string `json:"extensions" yaml:"extensions"`
	FolderAliases []string `json:"folder_aliases" yaml:"folder_aliases"`
}

// Library holds the static configuration for a run.
type Library struct {
	Profiles map[string]Profile
	romExts  map[string]bool
	Curated  map[string][]Entry
}

// Entry is one reviewed pick from curation.yaml.
type Entry struct {
	Title         string   `json:"title" yaml:"title"`
	Aliases       []string `json:"aliases" yaml:"aliases"`
	PlayMode      string   `json:"play_mode" yaml:"play_mode"`
	Genre         string   `json:"genre" yaml:"genre"`
	Reason        string   `json:"reason" yaml:"reason"`
	FamilyNotes   string   `json:"family_notes" yaml:"family_notes"`
	Popularity    string   `json:"popularity" yaml:"popularity"`
	Sources       []any    `json:"sources" yaml:"sources"`
	AllowJapanese bool     `json:"allow_japanese,omitempty" yaml:"allow_japanese,omitempty"`
	Mickey        bool     `json:"mickey,omitempty" yaml:"mickey,omitempty"`
}

// Load reads consoles.yaml from the data directory.
func Load(dataDir string) (*Library, error) {
	l := &Library{romExts: map[string]bool{}}
	if err := util.ReadYAML(filepath.Join(dataDir, "consoles.yaml"), &l.Profiles); err != nil {
		return nil, err
	}
	for _, p := range l.Profiles {
		for _, e := range p.Extensions {
			l.romExts[e] = true
		}
	}
	return l, nil
}

// LoadCuration reads curation.yaml.
func (l *Library) LoadCuration(dataDir string) error {
	return util.ReadYAML(filepath.Join(dataDir, "curation.yaml"), &l.Curated)
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
