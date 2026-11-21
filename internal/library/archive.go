package library

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

// Member is one regular file inside an archive.
type Member struct {
	Name string
	Size int64
}

type visitFunc func(m Member, open func() (io.Reader, error)) error

// walkArchive visits every regular entry. The format is chosen from the
// extension so an embedded archive can never be mistaken for the outer one.
// Entries that are links or special files abort the walk.
func walkArchive(path string, visit visitFunc) error {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return walkZip(path, visit)
	case strings.HasSuffix(lower, ".7z"):
		return walk7z(path, visit)
	case strings.HasSuffix(lower, ".rar"):
		return walkRar(path, visit)
	case strings.HasSuffix(lower, ".tar"), strings.HasSuffix(lower, ".tgz"),
		strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tar.bz2"):
		return walkTar(path, visit)
	}
	return fmt.Errorf("unsupported archive format: %s", filepath.Base(path))
}

func walkZip(path string, visit visitFunc) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		mode := f.Mode()
		if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if !mode.IsRegular() {
			return errors.New("Archive contains a link or special entry: " + f.Name)
		}
		f := f
		err := visit(Member{f.Name, int64(f.UncompressedSize64)}, func() (io.Reader, error) { return f.Open() })
		if err != nil {
			return err
		}
	}
	return nil
}

func walk7z(path string, visit visitFunc) error {
	r, err := sevenzip.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		mode := f.Mode()
		if mode.IsDir() {
			continue
		}
		if !mode.IsRegular() {
			return errors.New("Archive contains a link or special entry: " + f.Name)
		}
		f := f
		err := visit(Member{f.Name, int64(f.UncompressedSize)}, func() (io.Reader, error) { return f.Open() })
		if err != nil {
			return err
		}
	}
	return nil
}

func walkRar(path string, visit visitFunc) error {
	r, err := rardecode.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.IsDir {
			continue
		}
		if !h.Mode().IsRegular() || h.LinkType != 0 {
			return errors.New("Archive contains a link or special entry: " + h.Name)
		}
		err = visit(Member{h.Name, h.UnPackedSize}, func() (io.Reader, error) { return r, nil })
		if err != nil {
			return err
		}
	}
}

func walkTar(path string, visit visitFunc) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var src io.Reader = f
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tgz"), strings.HasSuffix(lower, ".gz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		src = gz
	case strings.HasSuffix(lower, ".bz2"):
		src = bzip2.NewReader(f)
	}
	t := tar.NewReader(src)
	for {
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return errors.New("Archive contains a link or special entry: " + h.Name)
		}
		err = visit(Member{h.Name, h.Size}, func() (io.Reader, error) { return t, nil })
		if err != nil {
			return err
		}
	}
}

// ListArchive returns every regular member without extracting anything.
func ListArchive(path string) ([]Member, error) {
	var members []Member
	err := walkArchive(path, func(m Member, _ func() (io.Reader, error)) error {
		members = append(members, m)
		return nil
	})
	return members, err
}

// SafeMember converts an archive entry name into a safe relative path.
func SafeMember(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	parts := strings.Split(n, "/")
	if n == "" || strings.HasPrefix(n, "/") || parts[0] == "" || strings.Contains(parts[0], ":") {
		return "", errors.New("Unsafe member: " + name)
	}
	clean := parts[:0]
	for _, p := range parts {
		if p == ".." {
			return "", errors.New("Unsafe member: " + name)
		}
		if p == "" || p == "." {
			continue
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", errors.New("Unsafe member: " + name)
	}
	return filepath.Join(clean...), nil
}

// extractArchive writes members under dest; the archive never picks output paths.
func extractArchive(path, dest string, limit int64) error {
	var total int64
	return walkArchive(path, func(m Member, open func() (io.Reader, error)) error {
		rel, err := SafeMember(m.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := open()
		if err != nil {
			return err
		}
		if c, ok := src.(io.Closer); ok {
			defer c.Close()
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		n, err := io.Copy(out, io.LimitReader(src, limit-total+1))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		total += n
		if total > limit {
			return errors.New("Archive expanded beyond limit")
		}
		return nil
	})
}
