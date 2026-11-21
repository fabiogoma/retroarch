package device

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fabiogoma/retroarch/internal/util"
)

// RemoteROMRoot is where RetroArch keeps imported ROMs on the Apple TV.
var RemoteROMRoot = []string{"downloads", "ROMS"}

// ExportFile is one ROM listed in an export's selection.json.
type ExportFile struct {
	Path         string
	Bytes        int64
	ExportSHA256 string
}

type exportSummary struct {
	Consoles []struct {
		Console  string `json:"console"`
		Selected int    `json:"selected"`
	} `json:"consoles"`
}

type exportSelection struct {
	File         string `json:"file"`
	Bytes        int64  `json:"bytes"`
	ExportSHA256 string `json:"export_sha256"`
}

// LoadExport validates an export directory against its manifests.
func LoadExport(export string) (map[string]map[string]ExportFile, error) {
	var summary exportSummary
	if err := util.ReadJSON(filepath.Join(export, "summary.json"), &summary); err != nil {
		return nil, err
	}
	groups := map[string]map[string]ExportFile{}
	for _, report := range summary.Consoles {
		console := report.Console
		if _, dup := groups[console]; dup || !util.SafeName(console) {
			return nil, fmt.Errorf("Invalid or duplicate console: %q", console)
		}
		folder := filepath.Join(export, console)
		var entries []exportSelection
		if err := util.ReadJSON(filepath.Join(folder, "selection.json"), &entries); err != nil {
			return nil, err
		}
		if len(entries) != report.Selected {
			return nil, fmt.Errorf("Manifest count disagrees with summary: %s", console)
		}
		files := map[string]ExportFile{}
		for _, e := range entries {
			if _, dup := files[e.File]; dup || !util.SafeName(e.File) {
				return nil, fmt.Errorf("Invalid or duplicate ROM name: %q", e.File)
			}
			p := filepath.Join(folder, e.File)
			info, err := os.Lstat(p)
			if err != nil || !info.Mode().IsRegular() || info.Size() != e.Bytes {
				return nil, fmt.Errorf("Local ROM does not match manifest: %s", p)
			}
			files[e.File] = ExportFile{p, e.Bytes, e.ExportSHA256}
		}
		groups[console] = files
	}
	return groups, nil
}

// Sync uploads missing ROMs. It never deletes or overwrites remote files.
func Sync(server Remote, groups map[string]map[string]ExportFile, dryRun bool) error {
	type job struct {
		folder string
		file   ExportFile
	}
	var pending []job
	skipped := 0
	consoles := make([]string, 0, len(groups))
	for c := range groups {
		consoles = append(consoles, c)
	}
	sort.Strings(consoles)
	for _, console := range consoles {
		files := groups[console]
		if len(files) == 0 {
			continue
		}
		parts := append(append([]string{}, RemoteROMRoot...), console)
		folder, err := server.EnsureFolder(parts, dryRun)
		if err != nil {
			return err
		}
		remote := map[string]Entry{}
		if folder != "" {
			if remote, err = server.List(folder); err != nil {
				return err
			}
		}
		display := folder
		if display == "" {
			display = "/" + joinSlash(parts) + "/"
		}
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			file := files[name]
			existing, ok := remote[name]
			switch {
			case !ok:
				pending = append(pending, job{display, file})
			case existing.Size != nil && *existing.Size == file.Bytes:
				skipped++
			default:
				return fmt.Errorf("Remote name exists with a different size or type: %s%s", folder, name)
			}
		}
	}
	for _, j := range pending {
		sum, err := util.FileSHA256(j.file.Path)
		if err != nil {
			return err
		}
		if sum != j.file.ExportSHA256 {
			return fmt.Errorf("Local ROM hash does not match manifest: %s", j.file.Path)
		}
	}
	for _, j := range pending {
		name := filepath.Base(j.file.Path)
		if dryRun {
			fmt.Printf("Would upload %s%s\n", j.folder, name)
			continue
		}
		if err := server.Upload(j.folder, j.file.Path, j.file.Bytes); err != nil {
			return err
		}
		listing, err := server.List(j.folder)
		if err != nil {
			return err
		}
		if e, ok := listing[name]; !ok || e.Size == nil || *e.Size != j.file.Bytes {
			return fmt.Errorf("Upload verification failed: %s%s", j.folder, name)
		}
		fmt.Printf("Uploaded %s%s\n", j.folder, name)
	}
	action := "Uploaded"
	if dryRun {
		action = "Would upload"
	}
	fmt.Printf("%s %d ROMs. Skipped %d already present.\n", action, len(pending), skipped)
	return nil
}

func joinSlash(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "/"
		}
		out += p
	}
	return out
}
