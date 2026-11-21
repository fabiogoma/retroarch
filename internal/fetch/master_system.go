package fetch

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fabiogoma/retroarch/internal/library"
	"github.com/fabiogoma/retroarch/internal/util"
)

type downloadLog struct {
	File   string `json:"file"`
	Status string `json:"status"`
	URL    string `json:"url,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	Error  string `json:"error,omitempty"`
}

// MasterSystem fetches Master System titles missing from the local library.
func MasterSystem(root, roms, stateDir string) error {
	msRoot := filepath.Join(roms, "master system")
	dest := filepath.Join(msRoot, "archive.org supplements")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	resp, err := get(ArchiveBase + "/metadata/master_system")
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	var metadata struct {
		Files []struct {
			Name string `json:"name"`
			Size string `json:"size"`
			SHA1 string `json:"sha1"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return err
	}
	var pretty any
	json.Unmarshal(raw, &pretty)
	if err := util.WriteJSON(filepath.Join(stateDir, "master_system_metadata.json"), pretty); err != nil {
		return err
	}
	existing := map[string]bool{}
	filepath.WalkDir(msRoot, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(p), ".zip") {
			existing[library.Normalized(library.TitleOf(filepath.Base(p)))] = true
		}
		return nil
	})
	logs := []downloadLog{}
	for _, item := range metadata.Files {
		if !strings.HasSuffix(strings.ToLower(item.Name), ".zip") {
			continue
		}
		key := library.Normalized(library.TitleOf(item.Name))
		if existing[key] {
			continue
		}
		if filepath.Base(item.Name) != item.Name || strings.ContainsAny(item.Name, "/\\") {
			return fmt.Errorf("Unsafe remote name: %s", item.Name)
		}
		u := ArchiveBase + "/download/master_system/" + url.PathEscape(item.Name)
		fmt.Println("Downloading", item.Name)
		sum, err := fetchSupplement(u, filepath.Join(dest, item.Name), item.Size, item.SHA1)
		if err != nil {
			logs = append(logs, downloadLog{File: item.Name, Status: "error", Error: err.Error()})
			fmt.Println("ERROR", item.Name, err)
			var denied AccessDenied
			if errors.As(err, &denied) {
				fmt.Println("Access requires authorization; stopping rather than retrying other files.")
				break
			}
			continue
		}
		existing[key] = true
		logs = append(logs, downloadLog{File: item.Name, Status: "downloaded", URL: u, SHA1: sum})
	}
	if err := util.WriteJSON(filepath.Join(stateDir, "master_system_downloads.json"), logs); err != nil {
		return err
	}
	downloaded, failed := 0, 0
	for _, l := range logs {
		if l.Status == "downloaded" {
			downloaded++
		} else {
			failed++
		}
	}
	fmt.Println("Downloaded", downloaded, "new titles; errors", failed)
	return nil
}

func fetchSupplement(u, target, size, wantSHA1 string) (string, error) {
	tmp := target + ".download"
	defer os.Remove(tmp)
	resp, err := get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha1.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if want, _ := strconv.ParseInt(size, 10, 64); n != want {
		return "", errors.New("Size mismatch")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if wantSHA1 != "" && sum != wantSHA1 {
		return "", errors.New("SHA1 mismatch")
	}
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return "", errors.New("ZIP integrity failure")
	}
	defer zr.Close()
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err == nil {
			_, err = io.Copy(io.Discard, rc)
			rc.Close()
		}
		if err != nil {
			return "", errors.New("ZIP integrity failure")
		}
	}
	zr.Close()
	return sum, os.Rename(tmp, target)
}
