package tiltfile

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
)

// Acceptance (plan §7.8): per-worktree FileWatch scoping — worktree runs
// watch the worktree's file tree.
//
// Watched paths follow for free: target Dependencies resolve through
// starkit.AbsWorkingDir, which the worktree thread local re-roots at the
// worktree (plan §0 amendment). The gap pinned here is the global-ignore
// root: a worktree run's .tiltignore-derived ignore must be evaluated
// relative to the WORKTREE, not the main checkout. Otherwise every worktree
// FileWatch silently loses global ignore coverage — patterns are matched
// against main-repo-relative paths while the watched files live in the
// worktree.
//
// Seam: the loader produces TiltfileLoadResult.Tiltignore with LocalPath =
// the worktree checkout dir for worktree runs. Dockerignore.LocalPath is
// "the path to evaluate the dockerignore contents relative to" and flows
// unchanged through WatchInputs → globalIgnores → IgnoreDef.BasePath
// (internal/controllers/core/tiltfile/filewatch.go), so no WatchInputs
// changes are needed. The pattern SOURCE stays the root .tiltignore next to
// the shared root Tiltfile (plan §3: one Tiltfile shared by all runs; the
// main checkout's .tiltignore governs them all, and the worktree run must
// still reload when it changes).
//
// Composition into FileWatch specs is pinned separately in
// internal/controllers/core/tiltfile/worktree_filewatch_test.go.

// Main run: unchanged. .tiltignore next to the Tiltfile, based at the main
// checkout (classic behavior preserved, plan §3).
func TestWorktreeFileWatch_MainRunTiltignoreUnchanged(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `local_resource("x", "true")`)
	f.file(".tiltignore", "node_modules\n")

	f.load()
	assert.Equal(t, filepath.Dir(f.loadResult.ConfigFiles[0]), f.loadResult.Tiltignore.LocalPath,
		"main run ignores must stay based at the main checkout")
	assert.Equal(t, []string{"node_modules"}, f.loadResult.Tiltignore.Patterns)
}

// Worktree run: the .tiltignore patterns still come from the root .tiltignore
// (next to the shared root Tiltfile), but LocalPath — the base the patterns
// are evaluated against — is the worktree checkout dir. ConfigFiles stay the
// root Tiltfile + root .tiltignore, so the worktree run reloads when either
// changes.
func TestWorktreeFileWatch_WorktreeRunTiltignoreRootedAtWorktree(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `local_resource("x", "true")`)
	f.file(".tiltignore", "node_modules\n")

	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tf.Labels = map[string]string{"tilt.dev/worktree": "feat-auth"}
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)

	wtDir := filepath.Join(filepath.Dir(tf.Spec.Path), ".worktree", "feat-auth")
	assert.Equal(t, wtDir, tlr.Tiltignore.LocalPath,
		"worktree run ignores must be evaluated relative to the worktree")
	assert.Equal(t, []string{"node_modules"}, tlr.Tiltignore.Patterns,
		"patterns still come from the root .tiltignore next to the shared Tiltfile")
	assert.Equal(t,
		[]string{
			filepath.Join(filepath.Dir(tf.Spec.Path), ".tiltignore"),
			tf.Spec.Path,
		},
		tlr.ConfigFiles,
		"the worktree run re-executes the shared root Tiltfile and reloads on its change")
}

// Worktree run: the files watched for a worktree resource live inside the
// worktree checkout. local_resource deps resolve through AbsPath →
// AbsWorkingDir, re-rooted at the worktree for worktree runs (plan §0
// amendment); these deps become FileWatch WatchedPaths via ToFileWatchObjects.
func TestWorktreeFileWatch_WorktreeRunWatchedPathsInsideWorktree(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
local_resource("x", "true", deps=["web"], worktree=True)
`)

	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tf.Labels = map[string]string{"tilt.dev/worktree": "feat-auth"}
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)

	wtDir := filepath.Join(filepath.Dir(tf.Spec.Path), ".worktree", "feat-auth")
	require.Len(t, tlr.Manifests, 1)
	assert.Equal(t, []string{filepath.Join(wtDir, "web")},
		tlr.Manifests[0].LocalTarget().Deps,
		"worktree run watch deps must resolve inside the worktree checkout")
}

// Per-worktree FileWatch scoping (plan §7.8): target-level ignores.
//
// local_resource(ignore=...) becomes a FileWatch ignore whose BasePath is the
// directory of the executing file (localResource.threadDir). For worktree runs
// the executing file is the shared root Tiltfile in the MAIN checkout, so a
// naive filepath.Dir(CurrentExecPath) would base the ignore patterns at the
// main repo while the watched deps live in the worktree — the same failure
// class the .tiltignore re-rooting (above) fixes for global ignores.
// threadDir must follow AbsWorkingDir, which the worktree context re-roots
// at the worktree checkout.
func TestWorktreeFileWatch_LocalResourceIgnoreRootedAtWorktree(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
local_resource("x", "true", deps=["web"], ignore=["logs"], worktree=True)
`)

	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tf.Labels = map[string]string{"tilt.dev/worktree": "feat-auth"}
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)

	wtDir := f.JoinPath(".worktree", "feat-auth")
	require.Len(t, tlr.Manifests, 1)
	lt := tlr.Manifests[0].LocalTarget()
	require.Equal(t, []string{f.JoinPath(wtDir, "web")}, lt.Deps,
		"deps must resolve inside the worktree")

	var found bool
	for _, ig := range lt.FileWatchIgnores {
		if len(ig.Patterns) == 1 && ig.Patterns[0] == "logs" {
			found = true
			assert.Equal(t, wtDir, ig.BasePath,
				"local_resource ignore patterns must be based at the worktree, not the main checkout")
		}
	}
	assert.True(t, found, "expected the local_resource ignore= pattern in FileWatchIgnores")
}
