package kubernetesapply

import (
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func stampWorkloadClone(e k8s.K8sEntity, worktree string) k8s.K8sEntity {
	clone := e.DeepCopy()

	// The selector mutation is deliberate (plan §4.2): the clone must NOT
	// match the stable Service. It is done directly on the typed selector —
	// InjectLabels never adds NEW keys to selectors (it only fills keys that
	// already exist, to protect user labels), and the clone needs exactly
	// the new `worktree=<name>` key in both the pod template and selector.
	switch o := clone.Obj.(type) {
	case *appsv1.Deployment:
		stampPodTemplate(o.Spec.Selector.MatchLabels, &o.Spec.Template, worktree)
		o.Status = appsv1.DeploymentStatus{}
	case *appsv1.StatefulSet:
		stampPodTemplate(o.Spec.Selector.MatchLabels, &o.Spec.Template, worktree)
		o.Status = appsv1.StatefulSetStatus{}
	}

	meta := clone.Meta()
	meta.SetName(meta.GetName() + worktreeCloneSuffix(worktree))
	annotations := meta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[v1alpha1.AnnotationWorktree] = worktree
	meta.SetAnnotations(annotations)

	// YAML re-parsed from an apply command's stdout (and some charts) carries
	// server-assigned fields the apiserver rejects on create: uid,
	// resourceVersion, creationTimestamp, generation, and the workload
	// status (zeroed above). Clean() drops the remaining bookkeeping
	// (managedFields, last-applied-configuration annotation).
	clone.Meta().SetUID("")
	clone.Meta().SetResourceVersion("")
	clone.Meta().SetCreationTimestamp(metav1.Time{})
	clone.Meta().SetGeneration(0)
	clone.Clean()

	return clone
}

// stampPodTemplate adds the worktree label to the given selector and pod
// template of an already-deep-copied workload.
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

// worktreeServiceSelector mirrors the Service's own selector with the
// worktree label added. Keys must be copied verbatim — a Service may select
// on labels the workload's selector.matchLabels doesn't repeat (e.g. a
// template-only label), and dropping them would change what the clone
// Service routes to.
func worktreeServiceSelector(selector map[string]string, worktree string) map[string]string {
	out := make(map[string]string, len(selector)+1)
	for k, v := range selector {
		out[k] = v
	}
	out[worktreeLabelKey] = worktree
	return out
}

// serviceSelectsWorkload reports whether the Service's selector matches the
// workload's pod-template labels — i.e. the Service routes to this
// workload's pods and must get a clone Service alongside the workload
// clone. Kubernetes matches pods when every selector entry appears in the
// pod's labels (selector ⊆ pod labels), which is the direction checked
// here; matching against selector.matchLabels instead would miss Services
// selecting a subset of the pod labels (chart-shaped YAML, plan §12).
func serviceSelectsWorkload(selector, templateLabels map[string]string) bool {
	if len(selector) == 0 || len(templateLabels) == 0 {
		// An empty selector matches nothing here: selector-less Services
		// (manual Endpoints / ExternalName) don't route via pod labels.
		return false
	}
	for k, v := range selector {
		if got, ok := templateLabels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// cloneService returns a copy of the Service with the worktree-stamped
// selector, a suffixed name, and the worktree annotation. Server-assigned
// fields (clusterIP, nodePort allocations, status) are reset so the clone
// can be created fresh: they are either rejected on create or would collide
// with the allocations the stable Service already owns.
func cloneService(svc *v1.Service, worktree string) *v1.Service {
	clone := svc.DeepCopy()

	clone.Spec.Selector = worktreeServiceSelector(svc.Spec.Selector, worktree)
	clone.Spec.ClusterIP = ""
	clone.Spec.ClusterIPs = nil
	clone.Spec.HealthCheckNodePort = 0
	clone.Spec.IPFamilies = nil
	clone.Spec.IPFamilyPolicy = nil
	clone.Spec.AllocateLoadBalancerNodePorts = nil
	for i := range clone.Spec.Ports {
		clone.Spec.Ports[i].NodePort = 0
	}
	clone.Status = v1.ServiceStatus{}

	meta := clone.ObjectMeta
	meta.SetName(meta.GetName() + worktreeCloneSuffix(worktree))
	annotations := meta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[v1alpha1.AnnotationWorktree] = worktree
	meta.SetAnnotations(annotations)
	clone.ObjectMeta = meta

	k8s.NewK8sEntity(clone).Clean()

	return clone
}

// stampWorktreeClones returns the entities to apply for a worktree run:
// the stable entities (untouched, in order) followed by one clone per
// Service selecting a stamped workload and one stamped clone per workload
// (clone Services before workload clones, so the clone Service exists when
// the clone pods are created). Chart-shaped YAML (plan §12) shares pod
// labels across sibling workloads, so a Service may select several
// workloads and still gets exactly one clone Service.
func stampWorktreeClones(entities []k8s.K8sEntity, worktree string) []k8s.K8sEntity {
	// Stable workload name -> pod-template labels, for Service matching.
	templateLabels := map[string]map[string]string{}
	for _, e := range entities {
		var labels map[string]string
		switch o := e.Obj.(type) {
		case *appsv1.Deployment:
			labels = o.Spec.Template.Labels
		case *appsv1.StatefulSet:
			labels = o.Spec.Template.Labels
		default:
			continue
		}
		templateLabels[e.Meta().GetName()] = labels
	}
	out := make([]k8s.K8sEntity, 0, len(entities))
	out = append(out, entities...)

	// Clone Services first, so they exist when the workload clones
	// create their pods.
	for _, e := range entities {
		svc, ok := e.Obj.(*v1.Service)
		if !ok {
			continue
		}
		matched := false
		for _, labels := range templateLabels {
			if serviceSelectsWorkload(svc.Spec.Selector, labels) {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, k8s.NewK8sEntity(cloneService(svc, worktree)))
		}
	}

	for _, e := range entities {
		// N.B. match on KIND, not just name: chart-shaped YAML commonly gives
		// the Service and the workload the same object name ("sancho"), so a
		// name-keyed lookup would stamp the Service as a workload clone too.
		switch e.Obj.(type) {
		case *appsv1.Deployment, *appsv1.StatefulSet:
			out = append(out, stampWorkloadClone(e, worktree))
		}
	}

	// Sibling DNS rewrite (plan §4.2 step 3), on the fully stamped set so
	// the clone-Service name set is complete before any refs resolve.
	rewriteSiblingRefs(out, worktree)

	return out
}

// scrubServerFields strips the server-assigned fields a re-parsed apply
// round-trip carries (uid, resourceVersion, creationTimestamp, generation)
// so the entities can be upserted on the user's behalf — the API server
// rejects them on create. Bookkeeping (managedFields, last-applied
// annotation) is dropped by Clean.
func scrubServerFields(entities []k8s.K8sEntity) {
	for _, e := range entities {
		e.Meta().SetUID("")
		e.Meta().SetResourceVersion("")
		e.Meta().SetCreationTimestamp(metav1.Time{})
		e.Meta().SetGeneration(0)
		e.Clean()
	}
}

// rewriteSiblingRefs applies the sibling DNS rewrite to the clone entities
// (plan §4.2 step 3): "the worktree clone's env/args get one transparent
// rewrite: refs to stable siblings stay as-is (shared, same namespace);
// refs to siblings that ARE worktree-flagged resolve to the clone names."
//
// Which siblings are worktree-flagged is derived from the same stamped set:
// a bare Service name whose clone Service (annotation tilt.dev/worktree) is
// in the set gets rewritten to the clone name. A Service clone is the only
// name that actually resolves to the sibling's clone pods, so only
// Service-backed refs are rewritten; refs to shared siblings — no clone in
// the set — stay bare and keep resolving through the stable Service. Only
// clone entities are touched; stable entities pass through untouched
// (one-way rewrite invariant, plan §12: shared objects never reference
// clones).
func rewriteSiblingRefs(entities []k8s.K8sEntity, worktree string) {
	suffix := worktreeCloneSuffix(worktree)

	// Bare names of the Services cloned in this set — the refs to rewrite.
	cloned := map[string]bool{}
	for _, e := range entities {
		if _, ok := e.Obj.(*v1.Service); !ok {
			continue
		}
		if e.Meta().GetAnnotations()[v1alpha1.AnnotationWorktree] != worktree {
			continue
		}
		if bare, ok := strings.CutSuffix(e.Meta().GetName(), suffix); ok {
			cloned[bare] = true
		}
	}
	if len(cloned) == 0 {
		return
	}

	for _, e := range entities {
		if e.Meta().GetAnnotations()[v1alpha1.AnnotationWorktree] != worktree {
			continue
		}
		pods, err := k8s.ExtractPods(e.Obj)
		if err != nil {
			continue
		}
		for _, pod := range pods {
			for i := range pod.Containers {
				rewriteContainerRefs(&pod.Containers[i], suffix, cloned)
			}
			for i := range pod.InitContainers {
				rewriteContainerRefs(&pod.InitContainers[i], suffix, cloned)
			}
		}
	}
}

// rewriteContainerRefs rewrites a clone container's env values and args in
// place.
func rewriteContainerRefs(c *v1.Container, suffix string, cloned map[string]bool) {
	for i := range c.Env {
		c.Env[i].Value = rewriteSiblingDNS(c.Env[i].Value, suffix, cloned)
	}
	for i := range c.Args {
		c.Args[i] = rewriteSiblingDNS(c.Args[i], suffix, cloned)
	}
}

// rewriteSiblingDNS rewrites one reference value: when the value's leading
// name is a cloned sibling, the clone name replaces it. Forms handled:
//
//	cache      -> cache-wt-feat-auth         (bare)
//	cache:6379 -> cache-wt-feat-auth:6379    (host:port)
//	cache.db   -> cache-wt-feat-auth.db      (dotted subdomain)
//
// Everything else passes through untouched: shared siblings (not in the
// clone set), scheme'd URLs (postgres://user@postgres/db), path forms, and
// arbitrary literals. A wrong rewrite breaks the app, so the rewrite only
// fires on values whose leading name is exactly a cloned sibling.
func rewriteSiblingDNS(value, suffix string, cloned map[string]bool) string {
	if strings.Contains(value, "://") {
		return value
	}
	head := value
	var sep, rest string
	if i := strings.IndexFunc(value, func(r rune) bool {
		return r == '.' || r == ':' || r == '/'
	}); i >= 0 {
		head, sep, rest = value[:i], string(value[i]), value[i+1:]
	}
	if head == "" || !cloned[head] {
		return value
	}
	switch {
	case sep == "":
		// The whole value is the bare name.
		return value + suffix
	case sep == ":" && isDigits(rest):
		return head + suffix + sep + rest
	case sep == ".":
		return head + suffix + sep + rest
	default:
		// Path forms and non-port colon forms: leave alone.
		return value
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
