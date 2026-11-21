// Package fetch downloads explicit Internet Archive files and pinned bezel artwork.
package fetch

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

// Tunables set from configuration by the cmd package.
var (
	MaxBytes        int64 = 32 << 30
	UserAgent             = "RetroArchAppleTV/1.0"
	ResponseTimeout       = 120 * time.Second
)

// ArchiveBase is the Internet Archive origin; tests replace it.
var ArchiveBase = "https://archive.org"

func newClient() *http.Client {
	return &http.Client{Timeout: 0, Transport: &http.Transport{ResponseHeaderTimeout: ResponseTimeout}}
}

// AccessDenied is returned for HTTP 401/403 so callers stop instead of retrying other files.
type AccessDenied struct{ URL string }

func (e AccessDenied) Error() string {
	return "Access denied; stopping. This source requires authorization."
}

func get(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := newClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		resp.Body.Close()
		return nil, AccessDenied{rawURL}
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return resp, nil
}

// Plan is one verified download.
type Plan struct {
	URL         string
	Destination string
	Size        int64 // -1 when unknown
	Hashes      map[string]string
}

// RelativeName validates a slash-separated relative path.
func RelativeName(value string) ([]string, error) {
	bad := value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/")
	for _, c := range value {
		bad = bad || c < 32
	}
	parts := strings.Split(value, "/")
	for _, p := range parts {
		bad = bad || p == "" || p == "." || p == ".." || strings.Contains(p, ":")
	}
	if bad {
		return nil, fmt.Errorf("Unsafe relative path: %q", value)
	}
	return parts, nil
}

// Destinations resolves manifest destinations against the project and ROMs roots.
type Destinations struct {
	Consoles map[string]bool
	Root     string // project root: bezels
	ROMs     string // consoles and artwork
}

func (d Destinations) resolve(name string) (string, error) {
	parts, err := RelativeName(name)
	if err != nil {
		return "", err
	}
	bezel := len(parts) == 3 && parts[0] == "rom_library" && parts[1] == "bezels" && strings.HasSuffix(parts[2], ".png")
	if !(d.Consoles[parts[0]] || parts[0] == "artwork" || bezel) {
		return "", errors.New("Destination must be inside a console folder, artwork/, or rom_library/bezels/<name>.png")
	}
	root := d.ROMs
	if parts[0] == "rom_library" {
		root = d.Root
	}
	if util.IsSymlink(root) {
		return "", fmt.Errorf("Symlink destination root: %s", root)
	}
	cur := root
	for _, p := range parts {
		cur = filepath.Join(cur, p)
		if util.IsSymlink(cur) {
			return "", fmt.Errorf("Symlink destination: %s", cur)
		}
	}
	return cur, nil
}

var itemRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ArchiveFile is one entry in an Internet Archive item's metadata.
type ArchiveFile map[string]any

// Metadata lists an Archive item's files.
func Metadata(item string) ([]ArchiveFile, error) {
	if !itemRe.MatchString(item) {
		return nil, fmt.Errorf("Invalid Internet Archive item identifier: %q", item)
	}
	resp, err := get(ArchiveBase + "/metadata/" + item)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data struct {
		Error any           `json:"error"`
		Files []ArchiveFile `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || data.Error != nil || data.Files == nil {
		return nil, fmt.Errorf("Internet Archive item unavailable: %s", item)
	}
	return data.Files, nil
}

func newHash(algorithm string) hash.Hash {
	switch algorithm {
	case "sha1":
		return sha1.New()
	case "md5":
		return md5.New()
	}
	return sha256.New()
}

func checksum(value any, algorithm string) (string, error) {
	s, _ := value.(string)
	if ok, _ := regexp.MatchString(fmt.Sprintf("^[0-9a-fA-F]{%d}$", newHash(algorithm).Size()*2), s); !ok {
		return "", fmt.Errorf("Invalid %s checksum: %v", algorithm, value)
	}
	return strings.ToLower(s), nil
}

// ManifestEntry is one explicit download request.
type ManifestEntry struct {
	Item        string `json:"item"`
	File        string `json:"file"`
	Destination string `json:"destination"`
	SHA256      string `json:"sha256"`
}

// ArchivePlan resolves a manifest against Archive metadata.
func ArchivePlan(raw []byte) ([]Plan, error) {
	var m struct {
		Version int             `json:"version"`
		Files   []ManifestEntry `json:"files"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Version != 1 {
		return nil, errors.New("Manifest must have version 1 and a files array")
	}
	if len(m.Files) == 0 {
		return nil, errors.New("Add explicit item/file/destination entries to the manifest files array")
	}
	cache := map[string][]ArchiveFile{}
	var plan []Plan
	for _, e := range m.Files {
		if _, err := RelativeName(e.File); err != nil {
			return nil, err
		}
		if _, err := RelativeName(e.Destination); err != nil {
			return nil, err
		}
		if _, ok := cache[e.Item]; !ok {
			files, err := Metadata(e.Item)
			if err != nil {
				return nil, err
			}
			cache[e.Item] = files
		}
		var matches []ArchiveFile
		for _, f := range cache[e.Item] {
			if f["name"] == e.File {
				matches = append(matches, f)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("File missing or ambiguous in Archive metadata: %s/%s", e.Item, e.File)
		}
		info := matches[0]
		var size int64
		switch v := info["size"].(type) {
		case string:
			if _, err := fmt.Sscanf(v, "%d", &size); err != nil {
				return nil, fmt.Errorf("Invalid size for %s", e.File)
			}
		case float64:
			size = int64(v)
		default:
			return nil, fmt.Errorf("Invalid size for %s", e.File)
		}
		if size < 0 || size > MaxBytes {
			return nil, fmt.Errorf("File size outside 0..%d: %s", MaxBytes, e.File)
		}
		hashes := map[string]string{}
		for _, a := range []string{"sha1", "md5"} {
			if v, ok := info[a]; ok && v != "" && v != nil {
				c, err := checksum(v, a)
				if err != nil {
					return nil, err
				}
				hashes[a] = c
			}
		}
		if e.SHA256 != "" {
			c, err := checksum(e.SHA256, "sha256")
			if err != nil {
				return nil, err
			}
			hashes["sha256"] = c
		}
		if len(hashes) == 0 {
			return nil, fmt.Errorf("No checksum available for %s/%s", e.Item, e.File)
		}
		plan = append(plan, Plan{ArchiveBase + "/download/" + e.Item + "/" + escapePath(e.File), e.Destination, size, hashes})
	}
	return plan, nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// BezelPlan lists the hash-pinned upstream console PNGs from bezels/sources.json.
func BezelPlan(dataDir string) ([]Plan, error) {
	var sources map[string]struct {
		Source string `json:"source"`
		SHA256 string `json:"sha256"`
	}
	if err := util.ReadJSON(filepath.Join(dataDir, "bezels", "sources.json"), &sources); err != nil {
		return nil, err
	}
	var plan []Plan
	for _, slug := range sortedKeys(sources) {
		e := sources[slug]
		if !strings.HasPrefix(e.Source, "https://raw.githubusercontent.com/libretro/overlay-borders/") {
			return nil, fmt.Errorf("Unexpected bezel source: %s", e.Source)
		}
		c, err := checksum(e.SHA256, "sha256")
		if err != nil {
			return nil, err
		}
		plan = append(plan, Plan{e.Source, "rom_library/bezels/" + slug + ".png", -1, map[string]string{"sha256": c}})
	}
	return plan, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func limit(p Plan) int64 {
	if p.Size >= 0 {
		return p.Size
	}
	return MaxBytes
}

func verify(path string, p Plan) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hashes := map[string]hash.Hash{}
	var writers []io.Writer
	for a := range p.Hashes {
		hashes[a] = newHash(a)
		writers = append(writers, hashes[a])
	}
	n, err := io.Copy(io.MultiWriter(writers...), io.LimitReader(f, limit(p)+1))
	if err != nil {
		return err
	}
	if n > limit(p) || (p.Size >= 0 && n != p.Size) {
		return fmt.Errorf("Size mismatch: %s", path)
	}
	for a, h := range hashes {
		if hex.EncodeToString(h.Sum(nil)) != p.Hashes[a] {
			return fmt.Errorf("Checksum mismatch: %s", path)
		}
	}
	return nil
}

// Download validates the whole plan, then fetches missing files without overwriting anything.
func Download(plan []Plan, d Destinations, dryRun bool) error {
	type job struct {
		path string
		plan Plan
	}
	seen := map[string]bool{}
	var pending []job
	for _, p := range plan {
		path, err := d.resolve(p.Destination)
		if err != nil {
			return err
		}
		key := strings.ToLower(path)
		if seen[key] {
			return fmt.Errorf("Duplicate destination: %s", path)
		}
		seen[key] = true
		if _, err := os.Lstat(path); err == nil {
			if err := verify(path, p); err != nil {
				return err
			}
			fmt.Println("Already verified:", path)
		} else {
			pending = append(pending, job{path, p})
		}
	}
	for _, j := range pending {
		if dryRun {
			fmt.Printf("Would download %s -> %s\n", j.plan.URL, j.path)
			continue
		}
		if _, err := d.resolve(j.plan.Destination); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
			return err
		}
		if err := fetchOne(j.path, j.plan, d); err != nil {
			return err
		}
		fmt.Println("Downloaded and verified:", j.path)
	}
	return nil
}

func fetchOne(path string, p Plan, d Destinations) error {
	// Random staging file prevents partial content from becoming a ROM.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".asset-*.download")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	resp, err := get(p.URL)
	if err != nil {
		tmp.Close()
		return err
	}
	defer resp.Body.Close()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, limit(p)+1))
	if err == nil && n > limit(p) {
		err = fmt.Errorf("Download larger than expected: %s", path)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := verify(tmp.Name(), p); err != nil {
		return err
	}
	if _, err := d.resolve(p.Destination); err != nil {
		return err
	}
	// Atomic, no-clobber activation; concurrent downloads cannot overwrite.
	return os.Link(tmp.Name(), path)
}

// ListItem prints an Archive item's file metadata as JSON.
func ListItem(item string) error {
	files, err := Metadata(item)
	if err != nil {
		return err
	}
	out, err := util.MarshalIndent(files)
	if err != nil {
		return err
	}
	fmt.Print(string(out))
	return nil
}
