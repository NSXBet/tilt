package worktree

import (
	"strings"

	"github.com/tilt-dev/tilt/pkg/model"
)

// Parse helpers for UI display (plan §9): engine names carry the worktree
// identity internally; these split them back out so the UI can show the
// author-visible name and group rows by worktree.

// SplitName splits an engine-internal manifest name into its worktree and
// author-visible parts. For a clone `wt:<worktree>_<name>` (underscore join —
// the engine-internal name doubles as apiserver object name, and
// BeforeCreate rejects '/' in names) it returns (worktree, base); for any
// other name (main-run manifests, the Tiltfile resource) it returns
// ("", name) unchanged.
func SplitName(name model.ManifestName) (worktree, base model.ManifestName) {
	s := string(name)
	rest, ok := strings.CutPrefix(s, namePrefix)
	if !ok {
		return "", name
	}
	wtStr, baseStr, found := strings.Cut(rest, "_")
	if !found {
		return "", name
	}
	return model.ManifestName(wtStr), model.ManifestName(baseStr)
}

// ParseTiltfileName parses the engine-internal name of a worktree Tiltfile
// resource (`tiltfile:<worktree>`, sourceTiltfilePrefix without a path
// segment). Returns ok=false for any other name.
func ParseTiltfileName(name model.ManifestName) (worktree string, ok bool) {
	return strings.CutPrefix(string(name), sourceTiltfilePrefix)
}
