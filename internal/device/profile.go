package device

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

// Roots are the RetroArch folders that make up a portable setup.
var Roots = []string{"config", "playlists", "downloads/ROMS", "thumbnails", "overlays", "shaders", "autoconfig", "assets", "system"}

var textSuffixes = map[string]bool{".cfg": true, ".opt": true, ".rmp": true, ".lpl": true, ".slangp": true, ".glslp": true, ".cgp": true}

// RE2 has no lookahead, so the trailing slash is captured and restored.
var (
	dataHome   = regexp.MustCompile(`(?:/private)?/var/mobile/Containers/Data/Application/[0-9A-Fa-f-]{36}(/)`)
	bundleHome = regexp.MustCompile(`(?:/private)?/var/(?:containers|mobile/Containers)/Bundle/Application/[0-9A-Fa-f-]{36}/[^/"\n]+\.app(/)`)
)

const mainConfig = "config/retroarch.cfg"

// Manifest describes a saved profile.
type Manifest struct {
	Version     int                  `json:"version"`
	CreatedUTC  string               `json:"created_utc"`
	Source      string               `json:"source"`
	Roots       []string             `json:"roots"`
	Directories []string             `json:"directories"`
	Files       map[string]FileEntry `json:"files"`
}

// FileEntry is a profile file's size and digest.
type FileEntry struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func relParts(value string) ([]string, error) { return util.RelativePath(value) }

// localPath refuses symlinks in every component, including the root itself.
func localPath(root, relative string) (string, error) {
	if util.IsSymlink(root) {
		return "", fmt.Errorf("Symlinks are not supported: %s", root)
	}
	parts, err := relParts(relative)
	if err != nil {
		return "", err
	}
	p := root
	for _, part := range parts {
		p = filepath.Join(p, part)
		if util.IsSymlink(p) {
			return "", fmt.Errorf("Symlinks are not supported: %s", p)
		}
	}
	return p, nil
}

func portable(p string) error {
	if !textSuffixes[strings.ToLower(filepath.Ext(p))] {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	content := string(data)
	updated := bundleHome.ReplaceAllString(dataHome.ReplaceAllString(content, "~$1"), ":$1")
	if updated != content {
		return os.WriteFile(p, []byte(updated), 0o644)
	}
	return nil
}

func remoteInventory(server Remote, roots []string) (map[string]int64, map[string]bool, error) {
	files, dirs := map[string]int64{}, map[string]bool{}
	var walk func(folder string) error
	walk = func(folder string) error {
		dirs[strings.Trim(folder, "/")] = true
		listing, err := server.List(folder)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(listing))
		for n := range listing {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			entry, p := listing[name], folder+name
			if _, err := relParts(strings.TrimLeft(p, "/")); err != nil {
				return err
			}
			switch {
			case entry.Path == p+"/":
				if err := walk(p + "/"); err != nil {
					return err
				}
			case entry.Path == p && entry.Size != nil && *entry.Size >= 0:
				files[strings.TrimLeft(p, "/")] = *entry.Size
			default:
				return fmt.Errorf("Invalid remote file or directory: %s", p)
			}
		}
		return nil
	}
	for _, root := range roots {
		parts, err := relParts(root)
		if err != nil {
			return nil, nil, err
		}
		folder, err := server.EnsureFolder(parts, true)
		if err != nil {
			return nil, nil, err
		}
		if folder != "" {
			if err := walk(folder); err != nil {
				return nil, nil, err
			}
		}
	}
	return files, dirs, nil
}

func localInventory(source string, roots []string) (map[string]int64, map[string]bool, error) {
	files, dirs := map[string]int64{}, map[string]bool{}
	var walk func(p, relative string) error
	walk = func(p, relative string) error {
		rel, _ := filepath.Rel(source, p)
		if _, err := localPath(source, filepath.ToSlash(rel)); err != nil {
			return err
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			dirs[relative] = true
			children, err := os.ReadDir(p)
			if err != nil {
				return err
			}
			for _, c := range children {
				if err := walk(filepath.Join(p, c.Name()), relative+"/"+c.Name()); err != nil {
					return err
				}
			}
		case info.Mode().IsRegular():
			files[relative] = info.Size()
		default:
			return fmt.Errorf("Unsupported local file: %s", p)
		}
		return nil
	}
	for _, root := range roots {
		p, err := localPath(source, root)
		if err != nil {
			return nil, nil, err
		}
		if _, err := os.Stat(p); err == nil {
			if err := walk(p, root); err != nil {
				return nil, nil, err
			}
		}
	}
	// Desktop-style data folders can keep the main cfg directly in the root.
	if _, ok := files[mainConfig]; !ok {
		p, err := localPath(source, "retroarch.cfg")
		if err != nil {
			return nil, nil, err
		}
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			files[mainConfig] = info.Size()
			dirs["config"] = true
		}
	}
	return files, dirs, nil
}

func contains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// BackupOptions control a profile snapshot.
type BackupOptions struct {
	Profile      string
	Source       string
	DryRun       bool
	IncludeSaves bool
}

// Backup saves a hash-verified, portable snapshot of the device (or a local source folder).
func Backup(server Remote, o BackupOptions) error {
	profile, err := filepath.Abs(o.Profile)
	if err != nil {
		return err
	}
	if _, err := localPath(filepath.Dir(profile), filepath.Base(profile)); err != nil {
		return err
	}
	previous := profile + ".previous"
	if _, err := localPath(filepath.Dir(previous), filepath.Base(previous)); err != nil {
		return err
	}
	roots := append([]string{}, Roots...)
	if o.IncludeSaves {
		roots = append(roots, "saves", "states")
	}
	var files map[string]int64
	var dirs map[string]bool
	source := ""
	if o.Source != "" {
		if source, err = filepath.Abs(o.Source); err != nil {
			return err
		}
		if source == profile || contains(source, profile) || contains(profile, source) {
			return fmt.Errorf("The source and profile directories must not contain each other")
		}
		files, dirs, err = localInventory(source, roots)
	} else {
		files, dirs, err = remoteInventory(server, roots)
	}
	if err != nil {
		return err
	}
	if _, ok := files[mainConfig]; !ok {
		return fmt.Errorf("Source has no config/retroarch.cfg; refusing an incomplete setup")
	}
	var total int64
	names := make([]string, 0, len(files))
	for n, s := range files {
		total += s
		names = append(names, n)
	}
	sort.Strings(names)
	verb := "Backing up"
	if o.DryRun {
		verb = "Would back up"
	}
	fmt.Printf("%s %d files (%d bytes) to %s\n", verb, len(files), total, profile)
	if o.DryRun {
		for _, n := range names {
			fmt.Println("Would back up /" + n)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(profile), 0o755); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(profile), ".retroarch-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	stage := filepath.Join(temp, "profile")
	data := filepath.Join(stage, "files")
	dirNames := make([]string, 0, len(dirs))
	for d := range dirs {
		dirNames = append(dirNames, d)
		if err := os.MkdirAll(filepath.Join(data, filepath.FromSlash(d)), 0o755); err != nil {
			return err
		}
	}
	sort.Strings(dirNames)
	manifest := Manifest{Version: 1, CreatedUTC: time.Now().UTC().Format(time.RFC3339Nano),
		Roots: roots, Directories: dirNames, Files: map[string]FileEntry{}}
	if source != "" {
		manifest.Source = source
	} else {
		manifest.Source = server.URL()
	}

	type captured struct {
		entry    FileEntry
		original [2]any
	}
	results := make([]captured, len(names))
	capture := func(i int) error {
		name, size := names[i], files[names[i]]
		dest := filepath.Join(data, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		var (
			actual int64
			hash   string
			err    error
		)
		if source != "" {
			original := filepath.Join(source, filepath.FromSlash(name))
			if _, err := os.Stat(original); err != nil && name == mainConfig {
				original = filepath.Join(source, "retroarch.cfg")
			}
			if err := copyPlain(original, dest); err != nil {
				return err
			}
			info, _ := os.Stat(dest)
			actual = info.Size()
			if hash, err = util.FileSHA256(dest); err != nil {
				return err
			}
		} else {
			for attempt := 0; attempt < 2; attempt++ {
				f, err := os.Create(dest)
				if err != nil {
					return err
				}
				actual, hash, err = server.Download("/"+name, f)
				f.Close()
				if err != nil {
					return err
				}
				if actual == size {
					break
				}
				if attempt == 0 {
					// History playlists can change after inventory; refresh the size and retry once.
					listing, err := server.List(path.Dir("/"+name) + slash(path.Dir("/"+name)))
					if err != nil {
						return err
					}
					latest, ok := listing[path.Base(name)]
					if !ok || latest.Path != "/"+name || latest.Size == nil {
						return fmt.Errorf("Source disappeared or changed type: /%s", name)
					}
					size = *latest.Size
				}
			}
		}
		if actual != size {
			return fmt.Errorf("Source changed or download was truncated: /%s", name)
		}
		if err := portable(dest); err != nil {
			return err
		}
		info, _ := os.Stat(dest)
		sum, err := util.FileSHA256(dest)
		if err != nil {
			return err
		}
		results[i] = captured{FileEntry{info.Size(), sum}, [2]any{actual, hash}}
		return nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		done     int
	)
	jobs := make(chan int)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				err := capture(i)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				done++
				if err == nil && (done%100 == 0 || done == len(names)) {
					fmt.Printf("Backed up %d/%d files\n", done, len(names))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range names {
		mu.Lock()
		stop := firstErr != nil
		mu.Unlock()
		if stop {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	var configOriginal [2]any
	for i, n := range names {
		manifest.Files[n] = results[i].entry
		if n == mainConfig {
			configOriginal = results[i].original
		}
	}
	var currentSize int64
	var currentHash string
	if source != "" {
		cfg := filepath.Join(source, mainConfig)
		if _, err := os.Stat(cfg); err != nil {
			cfg = filepath.Join(source, "retroarch.cfg")
		}
		info, err := os.Stat(cfg)
		if err != nil {
			return err
		}
		currentSize = info.Size()
		if currentHash, err = util.FileSHA256(cfg); err != nil {
			return err
		}
	} else if currentSize, currentHash, err = remoteDigest(server, "/"+mainConfig); err != nil {
		return err
	}
	if configOriginal[0] != currentSize || configOriginal[1] != currentHash {
		return fmt.Errorf("Main configuration changed during backup; leave RetroArch idle and retry")
	}
	if err := util.WriteJSON(filepath.Join(stage, "manifest.json"), manifest); err != nil {
		return err
	}
	// Validate the staged copy before replacing the last complete snapshot.
	if _, err := LoadProfile(stage); err != nil {
		return err
	}
	if _, err := os.Lstat(profile); err == nil {
		os.RemoveAll(previous)
		if err := os.Rename(profile, previous); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, profile); err != nil {
		if _, e := os.Lstat(profile); e != nil {
			os.Rename(previous, profile)
		}
		return err
	}
	fmt.Println("Saved reusable setup:", profile)
	return nil
}

func slash(p string) string {
	if p == "/" {
		return ""
	}
	return "/"
}

func copyPlain(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// LoadProfile validates a saved profile's manifest and every file hash.
func LoadProfile(profile string) (*Manifest, error) {
	mp, err := localPath(profile, "manifest.json")
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := util.ReadJSON(mp, &m); err != nil {
		return nil, err
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("Unsupported profile version")
	}
	allowed := append(append([]string{}, Roots...), "saves", "states")
	if len(m.Roots) == 0 {
		return nil, fmt.Errorf("Invalid profile roots")
	}
	for _, r := range m.Roots {
		ok := false
		for _, a := range allowed {
			ok = ok || a == r
		}
		if !ok {
			return nil, fmt.Errorf("Invalid profile roots")
		}
	}
	checked := func(name string) (string, error) {
		if _, err := relParts(name); err != nil {
			return "", err
		}
		inside := false
		for _, r := range m.Roots {
			inside = inside || name == r || strings.HasPrefix(name, r+"/")
		}
		if !inside {
			return "", fmt.Errorf("File is outside profile roots: %s", name)
		}
		return localPath(filepath.Join(profile, "files"), name)
	}
	for _, d := range m.Directories {
		p, err := checked(d)
		if err != nil {
			return nil, err
		}
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("Missing profile directory: %s", d)
		}
	}
	if _, ok := m.Files[mainConfig]; !ok {
		return nil, fmt.Errorf("Profile has no main RetroArch configuration")
	}
	for name, e := range m.Files {
		p, err := checked(name)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() != e.Bytes {
			return nil, fmt.Errorf("Profile hash or size mismatch: %s", name)
		}
		if sum, err := util.FileSHA256(p); err != nil || sum != e.SHA256 {
			return nil, fmt.Errorf("Profile hash or size mismatch: %s", name)
		}
	}
	return &m, nil
}

func randomID() string {
	return fmt.Sprintf("%x%x", time.Now().UnixNano(), os.Getpid())
}

// Restore uploads a saved profile, backing up and verifying every replaced file.
func Restore(server Remote, profile string, dryRun bool) error {
	m, err := LoadProfile(profile)
	if err != nil {
		return err
	}
	type digest struct {
		size int64
		sum  string
	}
	names := make([]string, 0, len(m.Files))
	for n := range m.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	var pending []string
	originals := map[string]digest{}
	listings := map[string]map[string]Entry{}
	// Plan every file before writing; same-size changes also need hashes.
	for _, name := range names {
		entry := m.Files[name]
		folder := path.Dir("/"+name) + "/"
		if _, ok := listings[folder]; !ok {
			avail, err := server.EnsureFolder(strings.Split(strings.Trim(folder, "/"), "/"), true)
			if err != nil {
				return err
			}
			listings[folder] = map[string]Entry{}
			if avail != "" {
				if listings[folder], err = server.List(folder); err != nil {
					return err
				}
			}
		}
		if existing, ok := listings[folder][path.Base(name)]; ok {
			if existing.Path != "/"+name || existing.Size == nil {
				return fmt.Errorf("Remote directory conflicts with file: /%s", name)
			}
			size, sum, err := remoteDigest(server, "/"+name)
			if err != nil {
				return err
			}
			if size == entry.Bytes && sum == entry.SHA256 {
				continue
			}
			originals[name] = digest{size, sum}
		}
		pending = append(pending, name)
	}
	for _, d := range m.Directories {
		if _, err := server.EnsureFolder(strings.Split(d, "/"), true); err != nil {
			return err
		}
	}
	verb := "Restoring"
	if dryRun {
		verb = "Would restore"
	}
	fmt.Printf("%s %d files; %d already identical.\n", verb, len(pending), len(names)-len(pending))
	if dryRun {
		for _, n := range pending {
			if _, ok := originals[n]; ok {
				fmt.Printf("Would replace (with backup) /%s\n", n)
			} else {
				fmt.Printf("Would upload /%s\n", n)
			}
		}
		return nil
	}

	// ROMs and dependencies first, playlists next, and main settings last.
	rank := func(n string) int {
		switch {
		case n == mainConfig:
			return 3
		case strings.HasPrefix(n, "config/"):
			return 2
		case strings.HasPrefix(n, "playlists/"):
			return 1
		}
		return 0
	}
	sort.Slice(pending, func(i, j int) bool {
		if ri, rj := rank(pending[i]), rank(pending[j]); ri != rj {
			return ri < rj
		}
		return pending[i] < pending[j]
	})
	dirs := append([]string{}, m.Directories...)
	sort.Slice(dirs, func(i, j int) bool {
		if ci, cj := strings.Count(dirs[i], "/"), strings.Count(dirs[j], "/"); ci != cj {
			return ci < cj
		}
		return dirs[i] < dirs[j]
	})
	ready := map[string]bool{}
	for _, d := range dirs {
		f, err := server.EnsureFolder(strings.Split(d, "/"), false)
		if err != nil {
			return err
		}
		ready[f] = true
	}
	runID := randomID()
	temp, err := os.MkdirTemp("", "retroarch-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	for i, name := range pending {
		remote := "/" + name
		folder := path.Dir(remote) + "/"
		if !ready[folder] {
			if folder, err = server.EnsureFolder(strings.Split(strings.Trim(folder, "/"), "/"), false); err != nil {
				return err
			}
			ready[folder] = true
		}
		entry := m.Files[name]
		original, err := localPath(filepath.Join(profile, "files"), name)
		if err != nil {
			return err
		}
		// Always stage under a unique name: the Apple server numbers duplicate uploads.
		staged := filepath.Join(temp, fmt.Sprintf("restore-%s-%d%s", runID, i, filepath.Ext(original)))
		if err := copyPlain(original, staged); err != nil {
			return err
		}
		info, _ := os.Stat(staged)
		if sum, _ := util.FileSHA256(staged); info.Size() != entry.Bytes || sum != entry.SHA256 {
			return fmt.Errorf("Profile changed during restore: %s", name)
		}
		if err := server.Upload(folder, staged, entry.Bytes); err != nil {
			return err
		}
		uploaded := folder + filepath.Base(staged)
		if size, sum, err := remoteDigest(server, uploaded); err != nil || size != entry.Bytes || sum != entry.SHA256 {
			return fmt.Errorf("Staged upload verification failed: %s", uploaded)
		}
		// Recheck before activation so in-memory config saves are not silently lost.
		listing, err := server.List(folder)
		if err != nil {
			return err
		}
		_, exists := listing[path.Base(remote)]
		orig, hadOriginal := originals[name]
		if hadOriginal {
			size, sum, err := remoteDigest(server, remote)
			if !exists || err != nil || size != orig.size || sum != orig.sum {
				return fmt.Errorf("Remote file changed during restore: %s", remote)
			}
		} else if exists {
			return fmt.Errorf("Remote file appeared during restore: %s", remote)
		}
		saved := ""
		if hadOriginal {
			saved = remote + ".restore-backup-" + runID
		}
		if err := activate(server, remote, saved, uploaded, folder, entry, orig.size, orig.sum); err != nil {
			return err
		}
		os.Remove(staged)
		fmt.Printf("Restored %d/%d: %s\n", i+1, len(pending), remote)
	}
	fmt.Println("Restore complete. Fully restart RetroArch to load the restored settings.")
	return nil
}

func activate(server Remote, remote, saved, uploaded, folder string, entry FileEntry, origSize int64, origSum string) (err error) {
	defer func() {
		if err != nil && saved != "" {
			// Roll back: put the original file back if the swap left it missing.
			if current, e := server.List(folder); e == nil {
				if _, has := current[path.Base(remote)]; !has {
					if _, ok := current[path.Base(saved)]; ok {
						server.Move(saved, remote)
					}
				}
			}
		}
	}()
	if saved != "" {
		if err = server.Move(remote, saved); err != nil {
			return err
		}
		if size, sum, e := remoteDigest(server, saved); e != nil || size != origSize || sum != origSum {
			return fmt.Errorf("Remote backup verification failed: %s", saved)
		}
		fmt.Println("Preserved", saved)
	}
	if err = server.Move(uploaded, remote); err != nil {
		return err
	}
	if size, sum, e := remoteDigest(server, remote); e != nil || size != entry.Bytes || sum != entry.SHA256 {
		return fmt.Errorf("Restore verification failed: %s", remote)
	}
	return nil
}
