// Package util holds small helpers shared by every subcommand.
package util

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
)

// SafeName reports whether name is a single, harmless path component.
func SafeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		if c == '/' || c == '\\' || c == '"' || c < 32 {
			return false
		}
	}
	return true
}

// FileSHA256 hashes a file's contents.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SHA256Hex hashes a byte slice.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MarshalIndent encodes v as 2-space indented JSON without HTML escaping.
func MarshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteJSON writes v as indented JSON with a trailing newline.
func WriteJSON(path string, v any) error {
	data, err := MarshalIndent(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadJSON decodes a JSON file into v.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// WriteYAML writes v as 2-space indented YAML.
func WriteYAML(path string, v any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ReadYAML decodes a YAML file into v.
func ReadYAML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Lock takes an exclusive, non-blocking lock so runs cannot race each other.
func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another run holds %s", path)
	}
	return func() { f.Close() }, nil
}

// IsSymlink reports whether path itself is a symbolic link.
func IsSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// RelativePath validates a slash-separated relative path.
func RelativePath(value string) ([]string, error) {
	parts := strings.Split(value, "/")
	if value == "" || strings.HasPrefix(value, "/") {
		return nil, fmt.Errorf("Unsafe relative path: %q", value)
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, ":\\") {
			return nil, fmt.Errorf("Unsafe relative path: %q", value)
		}
		for _, c := range p {
			if c < 32 {
				return nil, fmt.Errorf("Unsafe relative path: %q", value)
			}
		}
	}
	return parts, nil
}
