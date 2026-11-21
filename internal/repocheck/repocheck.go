// Package repocheck rejects tracked runtime data and binary assets before publishing.
package repocheck

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var rootFiles = map[string]bool{".gitignore": true, "README.md": true,
	"go.mod": true, "go.sum": true, "main.go": true, "retroarch-tool.yaml": true}

var configFiles = map[string]bool{
	"apple-tv.cfg":        true,
	"global.slangp":       true,
	"pce-six-button.opt":  true,
	"consoles.yaml":       true,
	"curation.yaml":       true,
	"apple_tv_cores.yaml": true,
}

func allowed(name string) bool {
	parts := strings.Split(name, "/")
	switch parts[0] {
	case "cmd":
		return len(parts) == 2 && strings.HasSuffix(name, ".go")
	case "internal":
		return strings.HasSuffix(name, ".go")
	case "config":
		return len(parts) == 2 && configFiles[parts[1]] ||
			name == "config/bezels/README.md" || name == "config/bezels/LICENSE" ||
			name == "config/bezels/sources.yaml" ||
			len(parts) >= 3 && parts[1] == "bezels" && strings.HasSuffix(name, ".cfg")
	case ".github":
		return name == ".github/copilot-instructions.md" || name == ".github/workflows/checks.yml"
	}
	return len(parts) == 1 && rootFiles[name]
}

// Check validates every git-tracked file in root.
func Check(root string) error {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	var errs []string
	count := 0
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" {
			continue
		}
		count++
		if !allowed(name) {
			errs = append(errs, name)
			continue
		}
		p := filepath.Join(root, name)
		info, err := os.Lstat(p)
		if err != nil {
			continue // tracked but deleted in the working tree
		}
		data, err := os.ReadFile(p)
		if info.Mode()&os.ModeSymlink != 0 || err != nil || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			errs = append(errs, name)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("Refusing runtime data, assets, or unexpected files:\n%s", strings.Join(errs, "\n"))
	}
	fmt.Printf("Checked %d tracked source/configuration files; no content assets.\n", count)
	return nil
}
