package kubernetesapply

import (
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/tilt-dev/tilt/internal/k8s"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// The apply interception pass for worktree runs (plan §4.1–4.2).
//
// One mutation stage over the parsed entities, next to InjectLabels in
// createEntitiesToDeploy — the choke point every YAML deploy funnels through
// (k8s_yaml, helm, kustomize, and ApplyCmd stdout re-parsed by the caller),
// so all of them get identical behavior by construction:
//
//   - workloads (Deployment/StatefulSet) are stamped as clones: name +
//     "-wt-<worktree>" suffix, `worktree=<name>` label in the pod template
//     AND the selector (deliberate — the clone must NOT match the stable
//     Service), plus the tilt.dev/worktree annotation
//   - each Service selecting a stamped workload gets a clone Service with
//     the mutated selector; cluster DNS <svc>-wt-<worktree> reaches only
//     worktree pods. The stable Service passes through untouched.
//   - everything else (ConfigMaps, Secrets, ...) is shared: passes through
//     uncloned
//   - the stable entities are never mutated: the worktree objects are
//     additive siblings (Rollout two-ReplicaSet-shapes-one-service insight,
//     implemented as sibling Deployments)
//
// The main run (worktree == "") is a no-op: classic behavior preserved.

// worktreeCloneSuffix joins a stable object name and the worktree name into
// the clone name: <stable>-wt-<worktree>.
func worktreeCloneSuffix(worktree string) string {
	return "-wt-" + worktree
}

// worktreeLabelKey is the pod-template/selector label key on workload
// clones: the bare `worktree` (plan §4.2).
const worktreeLabelKey = "worktree"

func stampWorkloadClone(e k8s.K8sEntity, worktree string) (k8s.K8sEntity, error) {
	clone := e.DeepCopy()

	// The selector mutation is deliberate (plan §4.2): the clone must NOT
	// match the stable Service. It is done directly on the typed selector —
	// InjectLabels never adds NEW keys to selectors (it only fills keys that
	// already exist, to protect user labels), and the clone needs exactly
	// the new `worktree=<name>` key in both the pod template and selector.
	stampPodTemplateSelectors(clone.Obj, worktree)
	clone.Meta().SetName(clone.Meta().GetName() + worktreeCloneSuffix(worktree))

	annotations := clone.Meta().GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[v1alpha1.AnnotationWorktree] = worktree
	clone.Meta().SetAnnotations(annotations)

	return clone, nil
}

// stampPodTemplateSelectors adds the worktree label to every pod template
// and workload selector of the object. Handles the typed Deployment and
// StatefulSet shapes parsed from user YAML; other kinds are returned as-is.
func stampPodTemplateSelectors(obj runtime.Object, worktree string) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		stampPodTemplate(o.Spec.Selector.MatchLabels, &o.Spec.Template, worktree)
	case *appsv1.StatefulSet:
		stampPodTemplate(o.Spec.Selector.MatchLabels, &o.Spec.Template, worktree)
	}
}

func stampPodTemplate(selector map[string]string, template *v1.PodTemplateSpec, worktree string) {
	if selector == nil {
		return
	}
	// The pod-template label key is the bare `worktree` (plan §4.2: "label
	// worktree=<name> in pod template AND selector"); the full
	// tilt.dev/worktree form stays reserved for the object annotation.
	if template != nil {
		if template.Labels == nil {
			template.Labels = map[string]string{}
		}
		template.Labels[worktreeLabelKey] = worktree
	}
	selector[worktreeLabelKey] = worktree
}

// worktree label — the selector a clone Service must use so cluster DNS
// <svc>-wt-<worktree> reaches only worktree pods.
func workloadSelector(selector map[string]string, worktree string) map[string]string {
	out := make(map[string]string, len(selector)+1)
	for k, v := range selector {
		out[k] = v
	}
	out[worktreeLabelKey] = worktree
	return out
}

// cloneService returns a copy of the Service with the given (already
// worktree-stamped) selector, a suffixed name, and the worktree annotation.
func cloneService(svc *v1.Service, selector map[string]string, worktree string) *v1.Service {
	clone := svc.DeepCopy()
	clone.Spec.Selector = make(map[string]string, len(selector))
	for k, v := range selector {
		clone.Spec.Selector[k] = v
	}
	meta := svc.ObjectMeta
	meta.SetName(meta.GetName() + worktreeCloneSuffix(worktree))

	annotations := meta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[v1alpha1.AnnotationWorktree] = worktree
	meta.SetAnnotations(annotations)
	clone.ObjectMeta = meta

	return clone
}

// workloadNameOf returns the object name when the entity is a cloneable
// workload (Deployment/StatefulSet), else "".
func workloadNameOf(e k8s.K8sEntity) string {
	switch e.Obj.(type) {
	case *appsv1.Deployment, *appsv1.StatefulSet:
		return e.Meta().GetName()
	}
	return ""
}

// workloadMatchesService reports whether the Service's selector matches the
// workload's (unmutated) matchLabels — i.e. the Service selects this
// workload and must get a clone Service alongside the workload clone.
func workloadMatchesService(svc *v1.Service, matchLabels map[string]string) bool {
	if len(svc.Spec.Selector) == 0 || len(matchLabels) == 0 {
		return false
	}
	for k, v := range matchLabels {
		if got, ok := svc.Spec.Selector[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// stampWorktreeClones returns the entities to apply for a worktree run: the
// original entities (stable, untouched) plus one stamped clone per workload
// and one clone per Service selecting a stamped workload.
func stampWorktreeClones(entities []k8s.K8sEntity, worktree string) ([]k8s.K8sEntity, error) {
	// Stable name -> workload matchLabels, for Service clone matching.
	matchLabels := map[string]map[string]string{}
	for _, e := range entities {
		var m map[string]string
		switch obj := e.Obj.(type) {
		case *appsv1.Deployment:
			m = obj.Spec.Selector.MatchLabels
		case *appsv1.StatefulSet:
			m = obj.Spec.Selector.MatchLabels
		default:
			continue
		}
		if len(m) > 0 {
			matchLabels[e.Meta().GetName()] = m
		}
	}

	cloneCount := len(matchLabels)
	svcClones := make([]k8s.K8sEntity, 0, cloneCount)
	if cloneCount > 0 {
		for _, e := range entities {
			svc, ok := e.Obj.(*v1.Service)
			if !ok {
				continue
			}
			for _, labels := range matchLabels {
				if !workloadMatchesService(svc, labels) {
					continue
				}
				clone := cloneService(svc, workloadSelector(labels, worktree), worktree)
				svcClones = append(svcClones, k8s.NewK8sEntity(clone))
				break
			}
		}
	}

	out := make([]k8s.K8sEntity, 0, len(entities)+cloneCount+len(svcClones))
	out = append(out, entities...)
	for _, e := range entities {
		// N.B. match on KIND, not just name: chart-shaped YAML commonly gives
		// the Service and the workload the same object name ("sancho"), so a
		// name-keyed lookup would stamp the Service as a workload clone too.
		if workloadNameOf(e) == "" {
			continue
		}
		if _, ok := matchLabels[e.Meta().GetName()]; !ok {
			continue
		}
		clone, err := stampWorkloadClone(e, worktree)
		if err != nil {
			return nil, err
		}
		out = append(out, clone)
	}
	out = append(out, svcClones...)

	return out, nil
}
