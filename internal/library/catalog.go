package library

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fabiogoma/retroarch/internal/util"
)

// RunCatalog scans every console folder, rebuilds the export and refreshes the CSV snapshots.
func RunCatalog(dataDir, roms string, count int, unzip bool) error {
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
	if err := lib.Scan(roms, state, tx); err != nil {
		return err
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
