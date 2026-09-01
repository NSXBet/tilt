package worktree

import (
	"os"
	"path/filepath"
)

func mkdir(dir string) error {
	return os.MkdirAll(filepath.FromSlash(dir), 0755)
}

// p is relative to root, slash-separated. Dirs are created as needed.
func writeFile(root, p, contents string) error {
	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(contents), 0644)
}

func join(parts ...string) string {
	return filepath.Join(parts...)
}
