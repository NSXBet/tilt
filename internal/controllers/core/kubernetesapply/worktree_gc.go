package kubernetesapply

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/tilt-dev/tilt/internal/k8s"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/logger"
)

// The clone GC pass (plan §9.1): prune clone objects whose owning worktree
// Tiltfile CR is gone — e.g. the worktree dir was deleted while `tilt up`
// was running.
//
// Ownership is annotation-keyed (plan §4.2): every clone carries the
// `tilt.dev/worktree` annotation naming the worktree run that stamped it,
// and that run's Tiltfile CR is named `tiltfile:<worktree>`
// (apis/tiltfile.WorktreeTiltfile). On each reconcile this pass lists the
// live Tiltfile CRs, records their worktree names as seen, and — when a
// SEEN worktree's CR is no longer in the list — deletes every annotated
// clone of that name.
//
// Scoping to seen names is deliberate: a worktree this process never
// observed cannot be distinguished from another Tilt process's worktree
// (one engine per repo, but clusters are shared), so only names this
// process saw alive and then observed vanish are pruned. A Tiltfile-list
// failure skips the pass entirely, so a transient partial list can never
// prune clones of a live worktree.

// worktreeCloneGVKs lists the kinds the clone contract (plan §4.2) stamps:
// workload clones and clone Services. Shared objects (ConfigMaps, Secrets)
// are never cloned, so there is nothing to prune for them.
var worktreeCloneGVKs = []schema.GroupVersionKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "apps", Version: "v1", Kind: "StatefulSet"},
	{Group: "", Version: "v1", Kind: "Service"},
}

// pruneOrphanClones deletes worktree clone objects whose owning worktree
// has no Tiltfile CR anymore (plan §9.1). Returns the number of entities
// deleted.
func (r *Reconciler) pruneOrphanClones(ctx context.Context) int {
	var list v1alpha1.TiltfileList
	err := r.ctrlClient.List(ctx, &list)
	if err != nil {
		// The GC pass is opportunistic: a failed list must not fail the
		// reconcile that triggered it, and must not look like "the CRs
		// are gone" — skip the pass. The next reconcile retries.
		logger.Get(ctx).Debugf("worktree GC: listing Tiltfile CRs: %v", err)
		return 0
	}

	live := map[string]bool{}
	for _, tf := range list.Items {
		if wt := tf.Labels[v1alpha1.LabelWorktree]; wt != "" {
			live[wt] = true
		}
	}

	r.mu.Lock()
	if r.worktreesSeen == nil {
		r.worktreesSeen = map[string]bool{}
	}
	// Names persist for the life of the process; only presence, never
	// absence, is recorded.
	for wt := range live {
		r.worktreesSeen[wt] = true
	}
	if len(r.worktreesSeen) == 0 {
		// Cheap early-out: a session that has never seen a worktree
		// (classic single-Tiltfile behavior) has no clones to prune.
		r.mu.Unlock()
		return 0
	}
	orphans := make([]string, 0, len(r.worktreesSeen))
	for wt := range r.worktreesSeen {
		if !live[wt] {
			orphans = append(orphans, wt)
		}
	}
	r.mu.Unlock()

	if len(orphans) == 0 {
		return 0
	}
	orphanSet := make(map[string]bool, len(orphans))
	for _, wt := range orphans {
		orphanSet[wt] = true
	}

	toDelete := make([]k8s.K8sEntity, 0)
	for _, gvk := range worktreeCloneGVKs {
		metas, err := r.k8sClient.ListMeta(ctx, gvk, "")
		if err != nil {
			// A missing kind (e.g. a cluster without apps/v1) means nothing
			// of that kind can be a clone; other list failures retry on the
			// next reconcile.
			if !apierrors.IsNotFound(err) {
				logger.Get(ctx).Debugf("worktree GC: listing %s: %v", gvk.Kind, err)
			}
			continue
		}
		for _, meta := range metas {
			wt, ok := meta.GetAnnotations()[v1alpha1.AnnotationWorktree]
			if !ok || !orphanSet[wt] {
				continue
			}
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(gvk)
			obj.SetName(meta.GetName())
			obj.SetNamespace(meta.GetNamespace())
			toDelete = append(toDelete, k8s.NewK8sEntity(obj))
		}
	}
	if len(toDelete) == 0 {
		return 0
	}

	names := make([]string, 0, len(toDelete))
	for _, e := range toDelete {
		names = append(names, e.Name())
	}
	logger.Get(ctx).Infof("Pruning worktree clones whose worktree is gone: %v", names)
	err = r.k8sClient.Delete(ctx, toDelete, 0)
	if err != nil {
		logger.Get(ctx).Errorf("Error pruning worktree clones: %v", err)
	}
	return len(toDelete)
}
