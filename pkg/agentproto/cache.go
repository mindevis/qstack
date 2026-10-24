package agentproto

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/mindevis/qstack/gen/go/qstack/agent/internalv1"
)

// The cache is one file holding the last desired state as a snapshot
// event; it is replaced atomically (write temp, fsync, rename, fsync dir),
// so a crash leaves either the old or the new state. Mode 0600: desired
// state may hold user data.

func loadCache(path string) (State, bool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is the agent's configured cache file
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("agentproto: read cache: %w", err)
	}
	ev := &agentv1.DesiredStateEvent{}
	if err := proto.Unmarshal(b, ev); err != nil || ev.GetSnapshot() == nil {
		return State{}, false, fmt.Errorf("agentproto: corrupt cache %s", path)
	}
	return stateFromSnapshot(ev), true, nil
}

func saveCache(path string, st State) error {
	b, err := proto.Marshal(st.snapshotEvent())
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("agentproto: cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("agentproto: write cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return fmt.Errorf("agentproto: write cache: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agentproto: write cache: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("agentproto: write cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agentproto: write cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("agentproto: write cache: %w", err)
	}
	if runtime.GOOS != "windows" { // directories cannot be fsynced on Windows
		if d, err := os.Open(dir); err == nil { //nolint:gosec // the cache directory
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}
