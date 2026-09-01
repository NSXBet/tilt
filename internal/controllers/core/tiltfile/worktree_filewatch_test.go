package tiltfile

import (
	"path/filepath"
	"testing"

	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Acceptance (plan §7.8): per-worktree FileWatch scoping, composed.
//
// For a worktree run the loader produces (see
// internal/tiltfile/worktree_filewatch_test.go):
//   - manifests whose target Dependencies live inside the worktree
//     (starkit.AbsWorkingDir re-rooted by the worktree thread local), and
//   - a Tiltignore with LocalPath = the worktree checkout dir.
//
// These tests pin that those inputs compose into FileWatch specs —
// WatchedPaths inside the worktree, global-ignore BasePath at the worktree —
// with no WatchInputs changes. The TiltfileManifestName follows the
// multi-Tiltfile naming (plan §2/§7.2: per-worktree CRs are
// "tiltfile:<worktree>").

// Worktree-run resource FileWatch: watched paths inside the worktree, global
// ignore base at the worktree.
func TestFileWatch_WorktreeRunResource(t *testing.T) {
	f := newFWFixture(t)
	wtDir := f.JoinPath(".worktree", "feat-auth")

	target := model.LocalTarget{
		Name: "web",
		Deps: []string{filepath.Join(wtDir, "src")},
	}
	f.SetManifestLocalTarget(target)
	f.inputs.Tiltignore = model.Dockerignore{LocalPath: wtDir, Patterns: []string{"node_modules"}}

	f.RequireFileWatchSpecEqual(target.ID(), v1alpha1.FileWatchSpec{
		WatchedPaths: []string{filepath.Join(wtDir, "src")},
		Ignores: []v1alpha1.IgnoreDef{
			{BasePath: wtDir, Patterns: []string{"node_modules"}},
		},
	})
}

// Worktree-run configs FileWatch: the worktree run re-executes the shared
// ROOT Tiltfile, so it watches the root Tiltfile and root .tiltignore, while
// its global ignores are still based at the worktree (loader-rooted
// Tiltignore.LocalPath).
func TestFileWatch_WorktreeRunConfigs(t *testing.T) {
	f := newFWFixture(t)
	wtDir := f.JoinPath(".worktree", "feat-auth")
	rootTiltfile := f.JoinPath("Tiltfile")

	f.inputs.TiltfileManifestName = model.ManifestName("tiltfile:feat-auth")
	f.inputs.TiltfilePath = rootTiltfile
	f.inputs.Tiltignore = model.Dockerignore{LocalPath: wtDir, Patterns: []string{"node_modules"}}

	id := model.TargetID{Type: model.TargetTypeConfigs, Name: model.TargetName("tiltfile:feat-auth")}
	f.RequireFileWatchSpecEqual(id, v1alpha1.FileWatchSpec{
		WatchedPaths: []string{rootTiltfile},
		Ignores: []v1alpha1.IgnoreDef{
			{BasePath: wtDir, Patterns: []string{"node_modules"}},
		},
	})
}
