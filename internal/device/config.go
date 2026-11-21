package device

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/fabiogoma/retroarch/internal/util"
)

// PushConfig replaces the device's main retroarch.cfg with a local file.
// The current remote file is saved locally and kept remotely before the swap.
func PushConfig(server Remote, local, backupDir string, dryRun bool) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("Refusing to push an empty config: %s", local)
	}
	current, err := Fetch(server, "/"+mainConfig)
	if err != nil {
		return err
	}
	if bytes.Equal(current, data) {
		fmt.Println("Device config already identical; nothing to do.")
		return nil
	}
	if dryRun {
		fmt.Printf("Would replace /%s with %s (%d -> %d bytes), keeping a backup.\n", mainConfig, local, len(current), len(data))
		return nil
	}

	runID := randomID()
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	backup := filepath.Join(backupDir, "retroarch-before-push-"+time.Now().UTC().Format("20060102T150405Z")+".cfg")
	if err := os.WriteFile(backup, current, 0o644); err != nil {
		return err
	}
	fmt.Println("Saved current device config to", backup)

	remote := "/" + mainConfig
	folder := path.Dir(remote) + "/"
	// Stage under a unique name: the Apple server numbers duplicate uploads.
	temp, err := os.MkdirTemp("", "retroarch-push-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	staged := filepath.Join(temp, "push-"+runID+".cfg")
	if err := os.WriteFile(staged, data, 0o644); err != nil {
		return err
	}
	entry := FileEntry{Bytes: int64(len(data)), SHA256: util.SHA256Hex(data)}
	if err := server.Upload(folder, staged, entry.Bytes); err != nil {
		return err
	}
	uploaded := folder + filepath.Base(staged)
	if size, sum, err := remoteDigest(server, uploaded); err != nil || size != entry.Bytes || sum != entry.SHA256 {
		return fmt.Errorf("Staged upload verification failed: %s", uploaded)
	}
	// Recheck so a config RetroArch saved meanwhile is not silently lost.
	again, err := Fetch(server, remote)
	if err != nil {
		return err
	}
	if !bytes.Equal(again, current) {
		return fmt.Errorf("Remote config changed during push; rerun to retry: %s", remote)
	}
	saved := remote + ".push-backup-" + runID
	if err := activate(server, remote, saved, uploaded, folder, entry, int64(len(current)), util.SHA256Hex(current)); err != nil {
		return err
	}
	fmt.Println("Pushed", local, "to", remote)
	fmt.Println("Fully restart RetroArch (without saving its old in-memory config) to load it.")
	return nil
}
