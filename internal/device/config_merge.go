package device

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

// MergeConfig updates keys from fragment into base while preserving all unrelated
// lines, comments, and ordering in base. Missing keys are appended at the end.
func MergeConfig(base, fragment []byte) []byte {
	desired := map[string]string{}
	var order []string
	for _, rawLine := range strings.Split(string(fragment), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, _, ok := splitConfigLine(line)
		if !ok {
			continue
		}
		if _, seen := desired[k]; !seen {
			order = append(order, k)
		}
		desired[k] = line
	}
	if len(desired) == 0 {
		return base
	}

	seen := map[string]bool{}
	baseText := strings.ReplaceAll(string(base), "\r\n", "\n")
	lines := strings.Split(baseText, "\n")
	// Preserve whether base ended with a trailing newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, rawLine := range lines {
		if k, _, ok := splitConfigLine(strings.TrimSpace(rawLine)); ok {
			if replacement, want := desired[k]; want {
				lines[i] = replacement
				seen[k] = true
			}
		}
	}
	for _, k := range order {
		if !seen[k] {
			lines = append(lines, desired[k])
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// ConfigChange describes a single key added or modified by MergeConfig.
type ConfigChange struct {
	Key    string
	OldVal string
	NewVal string
	Added  bool
}

// DiffConfig returns the keys added or modified between base and merged configs,
// in the order they appear in merged.
func DiffConfig(base, merged []byte) []ConfigChange {
	baseVals := map[string]string{}
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(base), "\r\n", "\n"), "\n") {
		if k, v, ok := splitConfigLine(strings.TrimSpace(rawLine)); ok {
			baseVals[k] = v
		}
	}
	var changes []ConfigChange
	seen := map[string]bool{}
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(merged), "\r\n", "\n"), "\n") {
		k, v, ok := splitConfigLine(strings.TrimSpace(rawLine))
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		oldVal, existed := baseVals[k]
		if !existed {
			changes = append(changes, ConfigChange{Key: k, NewVal: v, Added: true})
		} else if oldVal != v {
			changes = append(changes, ConfigChange{Key: k, OldVal: oldVal, NewVal: v})
		}
	}
	return changes
}

func splitConfigLine(line string) (key, val string, ok bool) {
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	idx := strings.IndexByte(line, '=')
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	val = strings.TrimSpace(line[idx+1:])
	if key == "" {
		return "", "", false
	}
	return key, val, true
}

// replaceRemoteFile safely uploads new bytes to a remote path using a unique staged
// filename and /move activation, because Apple TV's RetroArch web server numbers
// duplicate uploads instead of overwriting. If the target already exists, it is
// backed up remotely before swapping and rolled back if activation fails.
func replaceRemoteFile(server Remote, remote string, data []byte, backupTag string) error {
	folder := path.Dir(remote) + "/"
	if folder == "//" {
		folder = "/"
	}
	parts := strings.Split(strings.Trim(path.Dir(remote), "/"), "/")
	if len(parts) > 0 && parts[0] != "" {
		if _, err := server.EnsureFolder(parts, false); err != nil {
			return err
		}
	}

	listing, err := server.List(folder)
	if err != nil {
		return err
	}
	_, exists := listing[path.Base(remote)]

	var origSize int64
	var origSum string
	if exists {
		origSize, origSum, err = remoteDigest(server, remote)
		if err != nil {
			return err
		}
		if origSize == int64(len(data)) && origSum == util.SHA256Hex(data) {
			return nil
		}
	}

	runID := fmt.Sprintf("%x%x", time.Now().UnixNano(), os.Getpid())
	temp, err := os.MkdirTemp("", "retroarch-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)

	stagedName := fmt.Sprintf("stage-%s%s", runID, path.Ext(remote))
	stagedPath := filepath.Join(temp, stagedName)
	if err := os.WriteFile(stagedPath, data, 0o644); err != nil {
		return err
	}
	wantSum := util.SHA256Hex(data)
	wantSize := int64(len(data))

	if err := server.Upload(folder, stagedPath, wantSize); err != nil {
		return err
	}
	uploaded := folder + stagedName
	if sz, sum, err := remoteDigest(server, uploaded); err != nil || sz != wantSize || sum != wantSum {
		return fmt.Errorf("staged upload verification failed: %s", uploaded)
	}

	var saved string
	if exists && backupTag != "" {
		saved = remote + "." + backupTag + "-" + runID
		if err := server.Move(remote, saved); err != nil {
			return err
		}
		if sz, sum, err := remoteDigest(server, saved); err != nil || sz != origSize || sum != origSum {
			return fmt.Errorf("remote backup verification failed: %s", saved)
		}
	}

	if err := server.Move(uploaded, remote); err != nil {
		if saved != "" {
			if cur, e := server.List(folder); e == nil {
				if _, has := cur[path.Base(remote)]; !has {
					_ = server.Move(saved, remote)
				}
			}
		}
		return err
	}
	if sz, sum, err := remoteDigest(server, remote); err != nil || sz != wantSize || sum != wantSum {
		return fmt.Errorf("remote activation verification failed: %s", remote)
	}
	return nil
}
