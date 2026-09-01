package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// DefaultDir is where worktree checkouts live, relative to the repo root.
const DefaultDir = ".worktree"

// LabelWorktree marks a Tiltfile CR as a worktree re-execution of the shared
// root Tiltfile (plan §3); its value is the worktree name. No label means the
// main run.
const LabelWorktree = v1alpha1.LabelWorktree

// DirOf returns the checkout dir of the worktree named `name`, rooted at the
// directory of the shared root Tiltfile (plan §2: DefaultDir under the repo
// root). `name` must be non-empty.
func DirOf(rootTiltfilePath, name string) string {
	return filepath.Join(filepath.Dir(rootTiltfilePath), DefaultDir, name)
}

// A worktree discovered by position: a subdirectory of the worktree dir
// containing a Tiltfile, named by its directory basename.
type Worktree struct {
	// Name of the worktree: the directory basename.
	Name string
	// Absolute path of the worktree checkout.
	Dir string
}

// Discover scans root/dir ("" means DefaultDir) for worktrees: every
// subdirectory containing a Tiltfile is a worktree, named by its directory
// basename. The main checkout is the implicit worktree "main" and is NOT part
// of the result. A missing or empty worktree dir means no worktrees: classic
// single-Tiltfile behavior.
func Discover(root, dir string) ([]Worktree, error) {
	if dir == "" {
		dir = DefaultDir
	}
	wtDir := filepath.Join(root, dir)
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var wts []Worktree
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		wtDir := filepath.Join(wtDir, name)
		if _, err := os.Stat(filepath.Join(wtDir, "Tiltfile")); err != nil {
			continue
		}
		// A subdir named "main" collides with the implicit main worktree.
		if name == "main" {
			return nil, fmt.Errorf("worktree dir %q conflicts with the implicit main worktree; rename it", name)
		}
		wts = append(wts, Worktree{Name: name, Dir: wtDir})
	}

	sort.Slice(wts, func(i, j int) bool { return wts[i].Name < wts[j].Name })
	return wts, nil
}
