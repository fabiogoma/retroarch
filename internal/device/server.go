// Package device talks to RetroArch's built-in web file server on the Apple TV.
package device

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

// Entry is one item in a remote directory listing; directories have no Size.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size *int64 `json:"size,omitempty"`
}

// Remote is the subset of the file server used by every device task.
type Remote interface {
	URL() string
	List(folder string) (map[string]Entry, error)
	// Download streams a file to w and returns its byte count and SHA-256.
	Download(remote string, w io.Writer) (int64, string, error)
	Move(oldPath, newPath string) error
	// EnsureFolder creates the folder chain. In dry-run mode it returns "" when a folder is missing.
	EnsureFolder(parts []string, dryRun bool) (string, error)
	Upload(folder, localPath string, size int64) error
}

// Server is the HTTP implementation of Remote.
type Server struct {
	url    string
	client *http.Client
}

// Timeouts are set from configuration by the cmd package.
var (
	ConnectTimeout  = 30 * time.Second
	ResponseTimeout = 120 * time.Second
)

// NewServer returns a client for the given base URL.
func NewServer(rawURL string) *Server {
	return &Server{
		url: strings.TrimRight(rawURL, "/"),
		client: &http.Client{Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: ConnectTimeout}).DialContext,
			ResponseHeaderTimeout: ResponseTimeout,
		}},
	}
}

func (s *Server) URL() string { return s.url }

func (s *Server) check(resp *http.Response, err error) (*http.Response, error) {
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, resp.Request.URL.Path)
	}
	return resp, nil
}

func (s *Server) post(endpoint string, form url.Values) error {
	resp, err := s.check(s.client.PostForm(s.url+endpoint, form))
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func (s *Server) List(folder string) (map[string]Entry, error) {
	resp, err := s.check(s.client.Get(s.url + "/list?" + url.Values{"path": {folder}}.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	result := map[string]Entry{}
	for _, e := range entries {
		expected := folder + e.Name
		_, dup := result[e.Name]
		if !util.SafeName(e.Name) || dup || (e.Path != expected && e.Path != expected+"/") {
			return nil, fmt.Errorf("Invalid remote listing entry: %+v", e)
		}
		result[e.Name] = e
	}
	return result, nil
}

func (s *Server) Download(remote string, w io.Writer) (int64, string, error) {
	resp, err := s.check(s.client.Get(s.url + "/download?" + url.Values{"path": {remote}}.Encode()))
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

func (s *Server) Move(oldPath, newPath string) error {
	return s.post("/move", url.Values{"oldPath": {oldPath}, "newPath": {newPath}})
}

func (s *Server) EnsureFolder(parts []string, dryRun bool) (string, error) {
	folder := "/"
	for _, name := range parts {
		child := folder + name + "/"
		listing, err := s.List(folder)
		if err != nil {
			return "", err
		}
		entry, ok := listing[name]
		if !ok {
			if dryRun {
				return "", nil
			}
			if err := s.post("/create", url.Values{"path": {child}}); err != nil {
				return "", err
			}
			if listing, err = s.List(folder); err != nil {
				return "", err
			}
			entry, ok = listing[name]
		}
		if !ok || entry.Path != child {
			return "", fmt.Errorf("Remote path is not a directory: %s", child)
		}
		folder = child
	}
	return folder, nil
}

func (s *Server) Upload(folder, localPath string, size int64) error {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	boundary := hex.EncodeToString(raw[:])
	prefix := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"path\"\r\n\r\n%s\r\n--%s\r\nContent-Disposition: form-data; name=\"files[]\"; filename=\"%s\"\r\nContent-Type: application/octet-stream\r\n\r\n",
		boundary, folder, boundary, path.Base(strings.ReplaceAll(localPath, "\\", "/")))
	suffix := fmt.Sprintf("\r\n--%s--\r\n", boundary)
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	// The server needs an exact Content-Length, so stream without chunking.
	body := io.MultiReader(strings.NewReader(prefix), io.LimitReader(f, size), strings.NewReader(suffix))
	req, err := http.NewRequest(http.MethodPost, s.url+"/upload", body)
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(prefix)) + size + int64(len(suffix))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	resp, err := s.check(s.client.Do(req))
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Fetch downloads a remote file fully into memory.
func Fetch(r Remote, remote string) ([]byte, error) {
	var buf bytes.Buffer
	if _, _, err := r.Download(remote, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func remoteDigest(r Remote, remote string) (int64, string, error) {
	return r.Download(remote, io.Discard)
}
