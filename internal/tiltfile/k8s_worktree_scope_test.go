package tiltfile

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Acceptance (declarative scoping, plan §3): k8s_yaml(scope=) and
// k8s_resource(scope=) declare where manifests and their grouping/forwards
// instantiate across the main and worktree runs — replacing branching on
// worktree.name() for the k8s path, the same contract local_resource(scope=)
// already provides. Producers (helm/kustomize/local blobs) still render in
// every run; only ingestion is scoped.

const postgresYAML = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
spec:
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
      - name: postgres
        image: postgres:16
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
spec:
  selector:
    app: postgres
  ports:
  - port: 5432
`

// wtYAML is a per-checkout variant: a worktree is a full copy of the repo,
// so the same file paths exist inside .worktree/<name>/ with different
// content. Distinct names prove drops come from scope, not file absence.
const wtYAML = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wt-only
spec:
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
      - name: postgres
        image: postgres:16
---
apiVersion: v1
kind: Service
metadata:
  name: wt-only
spec:
  selector:
    app: postgres
  ports:
  - port: 5432
`

func loadWorktreeRun(f *fixture, name string) TiltfileLoadResult {
	tf := ctrltiltfile.WorktreeTiltfile(name, f.JoinPath("Tiltfile"), nil)
	return f.newTiltfileLoader().Load(f.ctx, tf, nil)
}

// scope="main": shared foundation. The worktree run never ingests it, so
// shared stateful infra (a database, a Secret) is instantiated exactly once
// and stays shared — PVCs never clone, so main-scoping is the only sound
// declaration for them.
func TestK8sYamlScope_MainDroppedInWorktreeRun(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `k8s_yaml('postgres.yaml', scope='main')`)
	f.file("postgres.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "postgres.yaml"), wtYAML)

	f.load()
	require.Len(t, f.loadResult.Manifests, 1, "main run instantiates main-scoped manifests")
	assert.Equal(t, model.ManifestName("postgres"), f.loadResult.Manifests[0].Name)

	tlr := loadWorktreeRun(f, "feat-auth")
	require.NoError(t, tlr.Error)
	require.Empty(t, tlr.Manifests, "main-scoped manifests must not instantiate in a worktree run")
}

// scope="worktree": branch-local manifests. The main run never sees them —
// nothing to skip with an if.
func TestK8sYamlScope_WorktreeDroppedInMainRun(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `k8s_yaml('api.yaml', scope='worktree')`)
	f.file("api.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "api.yaml"), wtYAML)

	f.load()
	require.Empty(t, f.loadResult.Manifests, "worktree-scoped manifests must not instantiate in the main run")

	tlr := loadWorktreeRun(f, "feat-auth")
	require.NoError(t, tlr.Error)
	require.Len(t, tlr.Manifests, 1, "worktree run instantiates worktree-scoped manifests")
}

// No scope (default "all"): the classic behavior — every run ingests the
// manifests; the engine's boundary pass clone-stamps worktree runs' output.
func TestK8sYamlScope_DefaultInstantiatesEverywhere(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `k8s_yaml('postgres.yaml')`)
	f.file("postgres.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "postgres.yaml"), postgresYAML)

	f.load()
	require.Len(t, f.loadResult.Manifests, 1)

	tlr := loadWorktreeRun(f, "feat-auth")
	require.NoError(t, tlr.Error)
	require.Len(t, tlr.Manifests, 1)
}

// helm output rides the same seam: k8s_yaml(helm(...), scope=...) scopes the
// chart's rendered manifests, so a branch-local chart needs no branching.
func TestK8sYamlScope_HelmOutputDroppedInWorktreeRun(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
k8s_yaml(helm('chart', name='wt-helm'), scope='main')
`)
	f.file("chart/Chart.yaml", chartYAML)
	f.file("chart/templates/deployment.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "chart/Chart.yaml"), chartYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "chart/templates/deployment.yaml"), wtYAML)


	f.load()
	require.Len(t, f.loadResult.Manifests, 1)

	tlr := loadWorktreeRun(f, "feat-auth")
	require.NoError(t, tlr.Error, "scoped helm output must not fail the worktree run")
	require.Empty(t, tlr.Manifests)
}

// Unknown scope values error loudly in every run — typo protection — instead
// of silently degrading to the default.
func TestK8sYamlScope_Invalid(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		f := newFixture(t)
		f.file("Tiltfile", `k8s_yaml('postgres.yaml', scope='banana')`)
		f.file("postgres.yaml", postgresYAML)
		f.loadErrString(`scope must be one of "main", "worktree", "all"`)
	})
	t.Run("worktree", func(t *testing.T) {
		f := newFixture(t)
		f.file("Tiltfile", `k8s_yaml('postgres.yaml', scope='banana')`)
		f.file("postgres.yaml", postgresYAML)
		f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")

		tlr := loadWorktreeRun(f, "feat-auth")
		require.Error(t, tlr.Error)
		assert.Contains(t, tlr.Error.Error(), `scope must be one of "main", "worktree", "all"`)
	})
}

// k8s_resource(scope=): a main-scoped resource's grouping/forwards drop from
// worktree runs — the call becomes a no-op there instead of failing assembly
// with "specified unknown resource" once its workload was scope-dropped.
func TestK8sResourceScope_MainDroppedInWorktreeRun(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
k8s_yaml('postgres.yaml', scope='main')
k8s_resource('postgres', port_forwards=['5433:5432'], scope='main')
`)
	f.file("postgres.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "postgres.yaml"), wtYAML)

	f.load()
	require.Len(t, f.loadResult.Manifests, 1)
	require.Len(t, f.loadResult.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards, 1)
	assert.EqualValues(t, 5433, f.loadResult.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards[0].LocalPort)
	assert.EqualValues(t, 5432, f.loadResult.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards[0].ContainerPort)

	tlr := loadWorktreeRun(f, "feat-auth")
	require.NoError(t, tlr.Error, "a scope-dropped k8s_resource must be a no-op in the worktree run")
	require.Empty(t, tlr.Manifests)
}

// Without scope, the pre-existing contract stands: a k8s_resource call whose
// workload was scope-dropped fails assembly — the error that made branching
// unavoidable before scope existed. Pinned so scope semantics stay opt-in.
func TestK8sResourceScope_UnscopedWorkloadDroppedErrors(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
k8s_yaml('postgres.yaml', scope='main')
k8s_resource('postgres', port_forwards=['5433:5432'])
`)
	f.file("postgres.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")

	tlr := loadWorktreeRun(f, "feat-auth")
	require.Error(t, tlr.Error)
	assert.Contains(t, tlr.Error.Error(), `k8s_resource specified unknown resource "postgres"`)
}

// A registry-allocated forward (local_port 0) instantiates in every run that
// names it — main and each worktree — and the reconciler deconflicts the
// local ports (portforward/registry). The load result keeps LocalPort 0.
func TestK8sResourceScope_AllocatedForwardInstantiatesEverywhere(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
k8s_yaml('postgres.yaml')
k8s_resource('postgres', port_forwards=[port_forward(0, container_port=5432)])
`)
	f.file("postgres.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "postgres.yaml"), postgresYAML)

	f.load()
	require.Len(t, f.loadResult.Manifests, 1)
	require.Len(t, f.loadResult.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards, 1)
	assert.EqualValues(t, 0, f.loadResult.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards[0].LocalPort)
	assert.EqualValues(t, 5432, f.loadResult.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards[0].ContainerPort)

	tlr := loadWorktreeRun(f, "feat-auth")
	require.NoError(t, tlr.Error)
	require.Len(t, tlr.Manifests, 1)
	require.Len(t, tlr.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards, 1)
	assert.EqualValues(t, 0, tlr.Manifests[0].K8sTarget().KubernetesApplySpec.PortForwardTemplateSpec.Forwards[0].LocalPort)
}

// Unknown scope values on k8s_resource error loudly in every run.
func TestK8sResourceScope_Invalid(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
k8s_yaml('postgres.yaml')
k8s_resource('postgres', scope='wrktree')
`)
	f.file("postgres.yaml", postgresYAML)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "postgres.yaml"), postgresYAML)

	f.loadErrString(`scope must be one of "main", "worktree", "all"`)

	tlr := loadWorktreeRun(f, "feat-auth")
	require.Error(t, tlr.Error)
	assert.Contains(t, tlr.Error.Error(), `scope must be one of "main", "worktree", "all"`)
}
