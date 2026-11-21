package library

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fabiogoma/retroarch/internal/util"
)

// RunCatalog scans every console folder (or reuses the existing catalog when fromCatalog is true),
// rebuilds the export and refreshes the CSV snapshots.
func RunCatalog(dataDir, roms string, count int, unzip, fromCatalog bool) error {
	if count < 1 {
		return fmt.Errorf("--count must be positive")
	}
	lib, err := Load(dataDir)
	if err != nil {
		return err
	}
	if err := lib.LoadCuration(dataDir); err != nil {
		return err
	}
	state := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		return err
	}
	// Refuse concurrent runs rather than race over extraction/export directories.
	unlock, err := util.Lock(filepath.Join(state, "run.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	db, err := Open(filepath.Join(state, "catalog.sqlite"))
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if fromCatalog {
		if err := loadExistingCatalog(roms, state, tx); err != nil {
			return err
		}
	} else {
		if err := lib.Scan(roms, state, tx); err != nil {
			return err
		}
	}
	reports, err := lib.Export(roms, state, tx, count, unzip)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	out, _ := util.MarshalIndent(reports)
	fmt.Print(string(out))
	if err := writeCSV(db, "SELECT * FROM games ORDER BY console,title", filepath.Join(state, "catalog.csv")); err != nil {
		return err
	}
	return writeCSV(db, "SELECT * FROM titles ORDER BY console,title", filepath.Join(state, "titles.csv"))
}

func loadExistingCatalog(roms, state string, db DB) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM games").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		csvPath := filepath.Join(state, "catalog.csv")
		if err := importCatalogCSV(csvPath, roms, state, db); err != nil {
			return fmt.Errorf("reuse catalog: %w (run without --from-catalog to perform a full scan)", err)
		}
	} else {
		if err := remapDBPaths(roms, state, db); err != nil {
			return err
		}
	}
	_, err := db.Exec("DELETE FROM selections")
	return err
}

func remapCatalogPath(p, roms, state string) string {
	if _, err := os.Stat(p); err == nil {
		return p
	}
	slashed := filepath.ToSlash(p)
	const extMarker = "/rom_library/state/extracted/"
	if idx := strings.Index(slashed, extMarker); idx >= 0 {
		return filepath.Join(state, "extracted", filepath.FromSlash(slashed[idx+len(extMarker):]))
	}
	const romsMarker = "/ROMs/"
	if idx := strings.LastIndex(slashed, romsMarker); idx >= 0 {
		return filepath.Join(roms, filepath.FromSlash(slashed[idx+len(romsMarker):]))
	}
	return p
}

func remapDBPaths(roms, state string, db DB) error {
	rows, err := db.Query("SELECT id,path FROM games")
	if err != nil {
		return err
	}
	type upd struct {
		id   int64
		path string
	}
	var updates []upd
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		if np := remapCatalogPath(p, roms, state); np != p {
			updates = append(updates, upd{id, np})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := db.Exec("UPDATE games SET path=? WHERE id=?", u.path, u.id); err != nil {
			return err
		}
	}
	return nil
}

func importCatalogCSV(csvPath, roms, state string, db DB) error {
	f, err := os.Open(csvPath)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return err
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	for _, req := range []string{"console", "title", "title_key", "path", "member", "sha256", "bytes", "region_score"} {
		if _, ok := col[req]; !ok {
			return fmt.Errorf("catalog.csv missing required column %q", req)
		}
	}
	imported := 0
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		bytesVal, err := strconv.ParseInt(rec[col["bytes"]], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid bytes %q: %w", rec[col["bytes"]], err)
		}
		regionVal, err := strconv.Atoi(rec[col["region_score"]])
		if err != nil {
			return fmt.Errorf("invalid region_score %q: %w", rec[col["region_score"]], err)
		}
		p := remapCatalogPath(rec[col["path"]], roms, state)
		if _, err := db.Exec(
			"INSERT OR IGNORE INTO games(console,title,title_key,path,member,sha256,bytes,region_score) VALUES (?,?,?,?,?,?,?,?)",
			rec[col["console"]], rec[col["title"]], rec[col["title_key"]], p, rec[col["member"]], rec[col["sha256"]], bytesVal, regionVal,
		); err != nil {
			return err
		}
		imported++
	}
	if imported == 0 {
		return errors.New("catalog.csv has no game rows")
	}
	return nil
}

func writeCSV(db *sql.DB, query, path string) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	w.Write(cols)
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			f.Close()
			return err
		}
		rec := make([]string, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
			case []byte:
				rec[i] = string(x)
			default:
				b, _ := json.Marshal(x)
				rec[i] = string(b)
				if s, ok := x.(string); ok {
					rec[i] = s
				}
			}
		}
		w.Write(rec)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	if err := rows.Err(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
