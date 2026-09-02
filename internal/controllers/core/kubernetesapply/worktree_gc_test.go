package kubernetesapply

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tilt-dev/tilt/internal/k8s"
	"github.com/tilt-dev/tilt/internal/k8s/testyaml"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// Clone GC acceptance (plan §9.1):
//
//   - clones whose `tiltfile:<worktree>` CR is gone are pruned
//     (annotation-keyed owner lookup, §4.2 + §9.1)
//   - clones of a worktree whose CR is alive are kept
//   - a worktree this process never observed alive is never swept: its
//     clones could belong to another Tilt process
//   - a failed Tiltfile-list lookup skips the pass (a transient failure
//     must not read as "the CR is gone")
//
// Clone pruning is keyed on the tilt.dev/worktree annotation the apply pass
// stamps (worktree_clone.go); the Tiltfile CRs come from the apiserver, and
// the deleted objects are read off the fake k8s client.

func wtCloneDeployment(name, worktree string, annotations map[string]string) k8s.K8sEntity {
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[v1alpha1.AnnotationWorktree] = worktree
	return k8s.K8sEntity{
		Obj: &appsv1.Deployment{
			TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name), Annotations: annotations},
		},
	}
}

func wtTiltfileCR(name, worktree string) *v1alpha1.Tiltfile {
	return &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "tiltfile:" + name,
			Labels: map[string]string{v1alpha1.LabelWorktree: worktree},
		},
	}
}

// A seen worktree whose CR vanished: its clones are pruned.
func TestWorktreeGCClonesOrphanedClones(t *testing.T) {
	f := newFixture(t)

	// The worktree's CR is alive on the first reconcile: the process observes it.
	f.Create(wtTiltfileCR("feat-auth", "feat-auth"))
	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec:       v1alpha1.KubernetesApplySpec{YAML: testyaml.SanchoYAML},
	}
	f.Create(&ka)
	f.MustReconcile(types.NamespacedName{Name: "a"})
	require.Empty(t, f.kClient.DeletedYaml)

	// Inject the clones the apply pass would have created, then delete the
	// worktree's CR (worktree dir deleted while running).
	f.kClient.Inject(
		wtCloneDeployment("sancho-wt-feat-auth", "feat-auth", nil),
		wtCloneDeployment("api-wt-feat-auth", "feat-auth", nil),
	)
	require.NoError(t, f.Client.Delete(f.Context(), wtTiltfileCR("feat-auth", "feat-auth")))
	f.MustReconcile(types.NamespacedName{Name: "a"})

	require.Contains(t, f.kClient.DeletedYaml, "sancho-wt-feat-auth")
	require.Contains(t, f.kClient.DeletedYaml, "api-wt-feat-auth")
}

// A live worktree's clones are never pruned; a never-seen worktree's clones
// are never pruned either (could belong to another Tilt process).
func TestWorktreeGCKeepsLiveAndForeignClones(t *testing.T) {
	f := newFixture(t)

	f.Create(wtTiltfileCR("feat-auth", "feat-auth"))
	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec:       v1alpha1.KubernetesApplySpec{YAML: testyaml.SanchoYAML},
	}
	f.Create(&ka)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	// feat-auth's CR is alive; "other" was never seen in this process.
	f.kClient.Inject(
		wtCloneDeployment("sancho-wt-feat-auth", "feat-auth", nil),
		wtCloneDeployment("sancho-wt-other", "other", nil),
	)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	assert.Empty(t, f.kClient.DeletedYaml)
}

// Without worktrees ever observed, the pass is a no-op — classic behavior
// touches no extra cluster state.
func TestWorktreeGC_NoWorktreesNoop(t *testing.T) {
	f := newFixture(t)

	ka := v1alpha1.KubernetesApply{
		ObjectMeta: metav1.ObjectMeta{Name: "a"},
		Spec:       v1alpha1.KubernetesApplySpec{YAML: testyaml.SanchoYAML},
	}
	f.Create(&ka)
	f.kClient.Inject(
		wtCloneDeployment("sancho-wt-feat-auth", "feat-auth", nil),
	)
	f.MustReconcile(types.NamespacedName{Name: "a"})

	assert.Empty(t, f.kClient.DeletedYaml)
}
