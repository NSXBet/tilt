package kubernetesapply

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tilt-dev/tilt/internal/k8s"
	"github.com/tilt-dev/tilt/internal/k8s/testyaml"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// Phase 2 acceptance: apply interception — the clone contract (plan §4.2, §11).
//
// Expected seam: KubernetesApplySpec.Worktree — the worktree this apply
// belongs to ("" = main run = no stamping). The worktree pass adds one
// mutation stage in createEntitiesToDeploy, so it covers k8s_yaml, helm and
// kustomize output identically, plus ApplyCmd-emitted YAML (both re-parse
// into the same pipeline).
//
// Clone contract for a worktree apply:
//   - workloads (Deployment/StatefulSet/...) are stamped as clones: name +
//     "-wt-<worktree>" suffix, label worktree=<name> in pod template AND
//     selector (selector mutation is deliberate — the clone must NOT match
//     the stable Service), annotation tilt.dev/worktree=<name>
//   - each Service selecting the workload gets a clone Service with the
//     mutated selector; the stable Service is untouched
//   - the stable entities pass through unmutated (additive siblings)
//   - non-workload objects (Secrets/ConfigMaps) are shared: never cloned

// Chart-shaped YAML in the incidents-admin style (plan §12: "chart sets
// matchLabels equal for all pods"): two Deployments + a Service all sharing
// `app: sancho` matchLabels, plus a shared ConfigMap.
const worktreeChartYAML = `
apiVersion: v1
kind: ConfigMap
metadata:
  name: sancho-config
data:
  key: value
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sancho
  labels:
    app: sancho
spec:
  replicas: 1
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
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sancho-sidecar
  labels:
    app: sancho
spec:
  replicas: 1
  selector:
    matchLabels:
      app: sancho
  template:
    metadata:
      labels:
        app: sancho
    spec:
      containers:
      - name: sidecar
        image: gcr.io/some-project-162817/sidecar
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

func deployEntities(t *testing.T, yaml string) []*appsv1.Deployment {
	t.Helper()
	entities, err := k8s.ParseYAMLFromString(yaml)
	require.NoError(t, err)
	var out []*appsv1.Deployment
	for _, e := range entities {
		if d, ok := e.Obj.(*appsv1.Deployment); ok {
			out = append(out, d)
		}
	}
	return out
}

func svcEntities(t *testing.T, yaml string) []*v1.Service {
	t.Helper()
	entities, err := k8s.ParseYAMLFromString(yaml)
	require.NoError(t, err)
	var out []*v1.Service
	for _, e := range entities {
		if s, ok := e.Obj.(*v1.Service); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestWorktreeCloneStamping(t *testing.T) {
	f := newFixture(t)
	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec: v1alpha1.KubernetesApplySpec{
			YAML:     worktreeChartYAML,
			Worktree: "feat-auth",
		},
	}
	f.Create(&ka)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	applied := f.kClient.Yaml
	require.Contains(t, applied, "sancho-wt-feat-auth", "clone must be applied")

	// Clones: both Deployments stamped, no matchLabels collision between
	// clone and stable (plan §12).
	clones := deployEntities(t, applied)
	require.Len(t, clones, 4, "each workload appears as stable + clone")
	byName := map[string]*appsv1.Deployment{}
	for _, d := range clones {
		byName[d.Name] = d
	}

	for _, name := range []string{"sancho", "sancho-sidecar"} {
		clone, ok := byName[name+"-wt-feat-auth"]
		require.True(t, ok, "missing clone %s-wt-feat-auth", name)
		assert.Equal(t, "feat-auth", clone.Spec.Template.Labels["worktree"],
			"clone %s pod template must carry worktree label", name)
		assert.Equal(t, "feat-auth", clone.Spec.Selector.MatchLabels["worktree"],
			"clone %s selector must carry worktree label (deliberate mutation)", name)
		assert.Equal(t, "feat-auth", clone.Annotations["tilt.dev/worktree"],
			"clone %s must carry the tilt.dev/worktree annotation", name)
	}

	// Stable untouched: bare names, no worktree annotation, selector without
	// the worktree label — the stable Service keeps matching only the stable.
	for _, name := range []string{"sancho", "sancho-sidecar"} {
		stable, ok := byName[name]
		require.True(t, ok, "stable %s must still be applied", name)
		_, hasAnn := stable.Annotations["tilt.dev/worktree"]
		assert.False(t, hasAnn, "stable %s must not be annotated", name)
		_, hasWt := stable.Spec.Selector.MatchLabels["worktree"]
		assert.False(t, hasWt, "stable %s selector must not gain the worktree label", name)
	}

	// Service clones: clone selector reaches ONLY worktree pods; stable
	// selector unchanged.
	svcs := svcEntities(t, applied)
	require.Len(t, svcs, 2, "stable Service + one clone Service")
	svcByName := map[string]*v1.Service{}
	for _, s := range svcs {
		svcByName[s.Name] = s
	}
	cloneSvc, ok := svcByName["sancho-wt-feat-auth"]
	require.True(t, ok, "missing clone Service")
	assert.Equal(t, "feat-auth", cloneSvc.Spec.Selector["worktree"])
	stableSvc, ok := svcByName["sancho"]
	require.True(t, ok, "stable Service must still be applied")
	assert.Equal(t, map[string]string{"app": "sancho"}, stableSvc.Spec.Selector,
		"stable Service selector must be untouched")

	// Shared objects never cloned.
	assert.Contains(t, applied, "name: sancho-config")
	assert.NotContains(t, applied, "sancho-config-wt-feat-auth")
}

// Inline YAML and ApplyCmd-emitted YAML funnel through the same choke point
// (plan §4.1) — both produce clones.
func TestWorktreeCloneStamping_ApplyCmdPath(t *testing.T) {
	f := newFixture(t)

	applyCmd, _ := f.createApplyCmd("custom-apply-cmd", testyaml.SanchoYAML)
	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec: v1alpha1.KubernetesApplySpec{
			Worktree: "feat-auth",
			ApplyCmd: &applyCmd,
			DeleteCmd: &v1alpha1.KubernetesApplyCmd{
				Args: []string{"custom-delete-cmd"},
			},
		},
	}
	f.Create(&ka)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	deployments := deployEntities(t, f.kClient.Yaml)
	require.Len(t, deployments, 2, "stable + clone")
	byName := map[string]*appsv1.Deployment{}
	for _, d := range deployments {
		byName[d.Name] = d
	}
	clone, ok := byName["sancho-wt-feat-auth"]
	require.True(t, ok)
	assert.Equal(t, "feat-auth", clone.Spec.Template.Labels["worktree"])
	assert.Equal(t, "feat-auth", clone.Spec.Selector.MatchLabels["worktree"])
	assert.Equal(t, "feat-auth", clone.Annotations["tilt.dev/worktree"])

	stable, ok := byName["sancho"]
	require.True(t, ok)
	_, hasAnn := stable.Annotations["tilt.dev/worktree"]
	assert.False(t, hasAnn)
}

// Main run (Worktree == "") must be a no-op: classic behavior preserved.
func TestWorktreeCloneStamping_MainRunNoop(t *testing.T) {
	f := newFixture(t)
	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec: v1alpha1.KubernetesApplySpec{
			YAML: testyaml.SanchoYAML,
		},
	}
	f.Create(&ka)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	deployments := deployEntities(t, f.kClient.Yaml)
	require.Len(t, deployments, 1)
	assert.Equal(t, "sancho", deployments[0].Name)
	assert.NotContains(t, f.kClient.Yaml, "wt-")
}

// Sibling DNS rewrite (plan §4.2 step 3): in clone entities, refs to
// siblings that are cloned in the same stamped set resolve to the clone
// names; refs to shared siblings stay bare.
func TestWorktreeSiblingDNSRewrite(t *testing.T) {
	for _, tt := range []struct {
		name     string
		yaml     string
		expected map[string]string // env value -> expected, on the api clone
	}{
		{
			name: "cloned sibling rewrites, shared stays bare",
			yaml: `
apiVersion: v1
kind: Service
metadata:
  name: cache
spec:
  selector:
    app: cache
  ports:
  - port: 6379
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cache
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cache
  template:
    metadata:
      labels:
        app: cache
    spec:
      containers:
      - name: cache
        image: redis
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
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
      - name: api
        image: api
        env:
        - name: CACHE_HOST
          value: cache
        - name: CACHE_ADDR
          value: cache:6379
        - name: CACHE_DOMAIN
          value: cache.default.svc.cluster.local
        - name: DB_HOST
          value: postgres
        - name: DB_URL
          value: postgres://user@postgres/db
        - name: NOT_A_HOST
          value: some literal
`,
			expected: map[string]string{
				"CACHE_HOST":   "cache-wt-feat-auth",
				"CACHE_ADDR":   "cache-wt-feat-auth:6379",
				"CACHE_DOMAIN": "cache-wt-feat-auth.default.svc.cluster.local",
				"DB_HOST":      "postgres",
				"DB_URL":       "postgres://user@postgres/db",
				"NOT_A_HOST":   "some literal",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			ka := v1alpha1.KubernetesApply{
				ObjectMeta: metav1.ObjectMeta{Name: "a"},
				Spec: v1alpha1.KubernetesApplySpec{
					YAML:     tt.yaml,
					Worktree: "feat-auth",
				},
			}
			f.Create(&ka)
			f.MustReconcile(types.NamespacedName{Name: "a"})

			clones := deployEntities(t, f.kClient.Yaml)
			var apiClone *appsv1.Deployment
			for _, d := range clones {
				if d.Name == "api-wt-feat-auth" {
					apiClone = d
				}
			}
			require.NotNil(t, apiClone, "api clone must be applied")

			env := map[string]string{}
			for _, ev := range apiClone.Spec.Template.Spec.Containers[0].Env {
				env[ev.Name] = ev.Value
			}
			for name, want := range tt.expected {
				assert.Equalf(t, want, env[name], "env %s", name)
			}

			// postgres has a Service but no workload in this set, so it is
			// NOT cloned: no clone Service, and the DB_HOST ref stays bare.
			assert.Contains(t, f.kClient.Yaml, "name: cache-wt-feat-auth")
			assert.NotContains(t, f.kClient.Yaml, "name: postgres-wt-feat-auth")
		})
	}
}

// Args rewrite: a clone's args referencing a cloned sibling resolve to the
// clone name; the stable sibling passes through untouched.
func TestWorktreeSiblingDNSRewrite_Args(t *testing.T) {
	yaml := `
apiVersion: v1
kind: Service
metadata:
  name: cache
spec:
  selector:
    app: cache
  ports:
  - port: 6379
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cache
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cache
  template:
    metadata:
      labels:
        app: cache
    spec:
      containers:
      - name: cache
        image: redis
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
      - name: api
        image: api
        args: ["--cache-host", "cache", "--db-host", "postgres"]
`
	f := newFixture(t)
	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec: v1alpha1.KubernetesApplySpec{
			YAML:     yaml,
			Worktree: "feat-auth",
		},
	}
	f.Create(&ka)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	var apiClone *appsv1.Deployment
	for _, d := range deployEntities(t, f.kClient.Yaml) {
		if d.Name == "api-wt-feat-auth" {
			apiClone = d
		}
	}
	require.NotNil(t, apiClone)
	assert.Equal(t,
		[]string{"--cache-host", "cache-wt-feat-auth", "--db-host", "postgres"},
		apiClone.Spec.Template.Spec.Containers[0].Args)
}

// Stable entities never reference clone names: the rewrite must not touch
// the stable Deployment's env (one-way invariant, plan §12).
func TestWorktreeSiblingDNSRewrite_StableUntouched(t *testing.T) {
	yaml := `
apiVersion: v1
kind: Service
metadata:
  name: cache
spec:
  selector:
    app: cache
  ports:
  - port: 6379
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cache
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cache
  template:
    metadata:
      labels:
        app: cache
    spec:
      containers:
      - name: cache
        image: redis
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
      - name: api
        image: api
        env:
        - name: CACHE_HOST
          value: cache
`
	f := newFixture(t)
	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec: v1alpha1.KubernetesApplySpec{
			YAML:     yaml,
			Worktree: "feat-auth",
		},
	}
	f.Create(&ka)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	for _, d := range deployEntities(t, f.kClient.Yaml) {
		if d.Name != "api" {
			continue
		}
		env := d.Spec.Template.Spec.Containers[0].Env
		require.Len(t, env, 1)
		assert.Equal(t, "cache", env[0].Value,
			"stable entity must keep the bare sibling ref")
	}
}
