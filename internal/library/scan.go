package library

import (
	"archive/zip"
	"crypto/sha256"
	"database/sql"
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

	_ "modernc.org/sqlite"
)

const schema = `CREATE TABLE IF NOT EXISTS archives(sha256 TEXT PRIMARY KEY,path TEXT,kind TEXT,members TEXT);
CREATE TABLE IF NOT EXISTS files(path TEXT PRIMARY KEY,console TEXT,kind TEXT,bytes INTEGER,mtime_ns INTEGER,status TEXT);
CREATE TABLE IF NOT EXISTS games(id INTEGER PRIMARY KEY,console TEXT,title TEXT,title_key TEXT,path TEXT,member TEXT,sha256 TEXT,bytes INTEGER,region_score INTEGER,UNIQUE(console,path,member));
CREATE TABLE IF NOT EXISTS selections(console TEXT,rank INTEGER,game_id INTEGER,title TEXT,details TEXT,PRIMARY KEY(console,rank));
CREATE TABLE IF NOT EXISTS issues(path TEXT,message TEXT);
CREATE VIEW IF NOT EXISTS titles AS SELECT console,title_key,MIN(title) AS title,COUNT(*) AS variants,COUNT(DISTINCT sha256) AS distinct_dumps FROM games GROUP BY console,title_key;`

// DB is satisfied by both *sql.DB and *sql.Tx.
type DB interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Open opens (creating if needed) the catalog database.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection keeps in-memory databases and transactions coherent.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

type archiveMember struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type candidate struct {
	path   string
	member string
}

var autoTop = regexp.MustCompile(`^AppleTV-Top[0-9]+$`)

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

func (l *Library) prepareArchive(path, state string, db DB, depth int) ([]candidate, error) {
	if depth > 8 {
		return nil, errors.New("Nested archive depth exceeds 8")
	}
	sha, _, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(state, "extracted", sha)
	complete := filepath.Join(dest, ".complete.json")
	var kind, membersJSON string
	row := db.QueryRow("SELECT kind,members FROM archives WHERE sha256=?", sha)
	have := row.Scan(&kind, &membersJSON) == nil
	_, completeErr := os.Stat(complete)
	var members []archiveMember
	if have && (kind == "single_zip" || completeErr == nil) {
		if err := json.Unmarshal([]byte(membersJSON), &members); err != nil {
			return nil, err
		}
	} else {
		listed, err := ListArchive(path)
		if err != nil {
			return nil, err
		}
		var total int64
		members = nil
		for _, m := range listed {
			members = append(members, archiveMember{m.Name, m.Size})
			total += m.Size
		}
		for _, m := range members {
			if _, err := SafeMember(m.Name); err != nil {
				return nil, err
			}
		}
		if total > maxExtract {
			return nil, errors.New("Archive exceeds 32 GiB extraction limit")
		}
		kind = "extract"
		if strings.EqualFold(filepath.Ext(path), ".zip") && len(members) == 1 &&
			l.romExts[strings.ToLower(filepath.Ext(members[0].Name))] {
			kind = "single_zip"
		}
		data, _ := json.Marshal(members)
		if _, err := db.Exec("INSERT OR REPLACE INTO archives VALUES (?,?,?,?)", sha, path, kind, string(data)); err != nil {
			return nil, err
		}
	}
	if kind == "single_zip" {
		return []candidate{{path, members[0].Name}}, nil
	}
	if _, err := os.Stat(complete); err != nil {
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
		meta, _ := json.Marshal(map[string]string{"source": path, "sha256": sha})
		if err := os.WriteFile(filepath.Join(temp, ".complete.json"), meta, 0o644); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(dest); err != nil {
			return nil, err
		}
		if err := os.Rename(temp, dest); err != nil {
			return nil, err
		}
	}
	var result []candidate
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
	for _, p := range files {
		ext := strings.ToLower(filepath.Ext(p))
		if archiveExts[ext] {
			sub, err := l.prepareArchive(p, state, db, depth+1)
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

// Scan rebuilds the catalog from every console folder under roms.
func (l *Library) Scan(roms, state string, db DB) error {
	for _, table := range []string{"files", "games", "issues", "selections"} {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	folders, err := os.ReadDir(roms)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, folder := range folders {
		name := folder.Name()
		if !folder.IsDir() || strings.HasPrefix(name, ".") || name == "rom_library" || autoTop.MatchString(name) {
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
			return err
		}
		sort.Strings(paths)
		for _, path := range paths {
			if err := l.scanFile(path, name, state, db, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Library) scanFile(path, folder, state string, db DB, seen map[string]bool) error {
	ext := strings.ToLower(filepath.Ext(path))
	status := "present"
	if ext == ".part" || ext == ".crdownload" || ext == ".download" {
		status = "incomplete"
	}
	kind := "auxiliary"
	if archiveExts[ext] {
		kind = "archive"
	} else if l.romExts[ext] {
		kind = "rom"
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if _, err := db.Exec("INSERT INTO files VALUES (?,?,?,?,?,?)", path, folder, kind, info.Size(), info.ModTime().UnixNano(), status); err != nil {
		return err
	}
	if kind == "auxiliary" {
		return nil
	}
	issue := func(err error) error {
		fmt.Println("ISSUE", path, err)
		_, e := db.Exec("INSERT INTO issues VALUES (?,?)", path, err.Error())
		return e
	}
	cands := []candidate{{path, ""}}
	if kind == "archive" {
		if cands, err = l.prepareArchive(path, state, db, 0); err != nil {
			return issue(err)
		}
	}
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
		var sha string
		var size int64
		if c.member != "" {
			sha, size, err = hashZipMember(c.path, c.member)
		} else {
			sha, size, err = hashFile(c.path)
		}
		if err != nil {
			return issue(err)
		}
		title := TitleOf(name)
		_, err = db.Exec("INSERT OR IGNORE INTO games(console,title,title_key,path,member,sha256,bytes,region_score) VALUES (?,?,?,?,?,?,?,?)",
			console, title, Normalized(title), c.path, c.member, sha, size, RegionScore(name))
		if err != nil {
			return err
		}
	}
	return nil
}
