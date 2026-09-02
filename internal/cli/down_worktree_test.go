package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tilt-dev/tilt/internal/controllers/core/kubernetesapply"
	"github.com/tilt-dev/tilt/internal/k8s"
	"github.com/tilt-dev/tilt/internal/k8s/testyaml"
	"github.com/tilt-dev/tilt/internal/tiltfile"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/model"
)

// `tilt down` worktree teardown acceptance (plan §9.1):
//
//   - with --worktrees (default), down re-executes the root Tiltfile per
//     discovered worktree and deletes their manifests alongside main's
//   - the clone objects (plan §4.2) each worktree run applies are deleted
//     too — recomputed with WorktreeClones from the run's YAML, so only the
//     clones THIS Tiltfile created are touched
//   - a worktree whose Tiltfile fails to load is skipped (it must not block
//     the rest of the teardown)
//   - with --worktrees=false, classic single-Tiltfile teardown

// worktreeTestYAML is a Deployment + matching Service — the shape that
// produces a workload clone and a clone Service.
const worktreeTestYAML = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sancho
spec:
  selector:
    matchLabels:
      app: sancho
  template:
    metadata:
      labels:
        app: sancho
    spec:
      containers:
      - name: sancho
        image: gcr.io/some-project-162817/sancho
---
apiVersion: v1
kind: Service
metadata:
  name: sancho
spec:
  selector:
    app: sancho
  ports:
  - port: 80
`

// writeWorktreeTiltfileRoot writes a root dir with a Tiltfile and one
// Tiltfile-bearing subdir per worktree — the position-based discovery input
// (plan §2).
func writeWorktreeTiltfileRoot(t *testing.T, worktrees ...string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Tiltfile"), []byte("# main"), 0644))
	for _, wt := range worktrees {
		wtDir := filepath.Join(dir, worktree.DefaultDir, wt)
		require.NoError(t, os.MkdirAll(wtDir, 0777))
		require.NoError(t, os.WriteFile(filepath.Join(wtDir, "Tiltfile"), []byte("# worktree"), 0644))
	}
	return dir
}

// recordingK8sClient captures every Delete payload across batches — the
// FakeK8sClient keeps only the last one.
type recordingK8sClient struct {
	k8s.Client
	t *testing.T

	mu      sync.Mutex
	deleted []string
}

func (c *recordingK8sClient) Delete(ctx context.Context, entities []k8s.K8sEntity, wait time.Duration) error {
	if len(entities) > 0 {
		yaml, err := k8s.SerializeSpecYAML(entities)
		require.NoError(c.t, err)
		c.mu.Lock()
		c.deleted = append(c.deleted, yaml)
		c.mu.Unlock()
	}
	return c.Client.Delete(ctx, entities, wait)
}

// recorder swaps a recording client into DownDeps before the first down()
// call.
func (f *downFixture) recorder() *recordingK8sClient {
	r := &recordingK8sClient{Client: f.kCli, t: f.t}
	f.deps.kClient = r
	return r
}

func (c *recordingK8sClient) deletedAll() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := ""
	for _, y := range c.deleted {
		out += y + "\n---\n"
	}
	return out
}

// Down deletes the main manifests plus every worktree's manifests, and the
// clone objects each worktree run applies.
func TestDownWorktrees(t *testing.T) {
	f := newDownFixture(t)

	root := writeWorktreeTiltfileRoot(t, "feat-a")
	f.cmd.fileName = filepath.Join(root, "Tiltfile")

	// Main run: one k8s manifest. Worktree runs: a Deployment + Service
	// (both the manifests and their -wt-<name> clones must be deleted).
	f.tfl.Result = newTiltfileLoadResult(newK8sManifest())
	wtManifest := model.Manifest{Name: "wt-fe"}.WithDeployTarget(k8s.MustTarget("wt-fe", worktreeTestYAML))
	f.tfl.Delegate = &tiltfile.FakeTiltfileLoader{Result: tiltfile.TiltfileLoadResult{
		Manifests: []model.Manifest{newK8sManifest(), wtManifest},
	}}

	f.cmd.worktrees = true
	rec := f.recorder()
	err := f.cmd.down(f.ctx, f.deps, nil)
	require.NoError(t, err)

	deleted := rec.deletedAll()
	// Main + worktree manifests (deduped: the shared sancho appears once per
	// delete batch) all deleted.
	assert.Contains(t, deleted, "name: sancho")
	// Worktree clone objects, recomputed from the run's YAML: the workload
	// clone and the clone Service.
	assert.Contains(t, deleted, "name: sancho-wt-feat-a")
}

// A worktree run whose Tiltfile errors contributes neither manifests nor
// clones; healthy worktrees still tear down.
func TestDownWorktreesSkipsBrokenWorktree(t *testing.T) {
	f := newDownFixture(t)

	runs := []worktreeRun{
		{name: "broken", tlr: tiltfile.TiltfileLoadResult{Error: fmt.Errorf("tiltfile broken")}},
		{name: "ok", tlr: newTiltfileLoadResult(
			model.Manifest{Name: "wt"}.WithDeployTarget(k8s.MustTarget("wt", testyaml.SanchoYAML)))},
	}
	rec := f.recorder()
	err := deleteWorktreeClones(f.ctx, runs, f.deps)
	require.NoError(t, err)

	deleted := rec.deletedAll()
	assert.NotContains(t, deleted, "wt-broken")
	assert.Contains(t, deleted, "sancho-wt-ok")
}

// With --worktrees=false, worktree manifests are not loaded or deleted:
// classic behavior.
func TestDownNoWorktreesFlag(t *testing.T) {
	f := newDownFixture(t)

	f.tfl.Result = newTiltfileLoadResult(newK8sManifest())
	f.cmd.worktrees = false
	err := f.cmd.down(f.ctx, f.deps, nil)
	require.NoError(t, err)

	assert.Contains(t, f.kCli.DeletedYaml, "sancho")
	assert.NotContains(t, f.kCli.DeletedYaml, "-wt-")
}

// The clone set is recomputed from the worktree run's YAML — the same
// entities the apply pass stamped (WorktreeClones contract): one workload
// clone for the Deployment plus one clone Service selecting its pods.
func TestDownWorktreeCloneNames(t *testing.T) {
	wtManifest := model.Manifest{Name: "wt-fe"}.WithDeployTarget(k8s.MustTarget("wt-fe", worktreeTestYAML))
	entities := manifestEntities([]model.Manifest{wtManifest})
	require.Len(t, entities, 2, "Deployment + Service parsed")

	clones, err := kubernetesapply.WorktreeClones(entities, "feat-auth")
	require.NoError(t, err)
	names := make([]string, 0, len(clones))
	for _, c := range clones {
		names = append(names, c.Name())
	}
	assert.ElementsMatch(t, []string{"sancho-wt-feat-auth", "sancho-wt-feat-auth"}, names)
}
