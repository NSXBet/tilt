package tiltfile

import "fmt"

// Scope values shared by every declaration that instantiates per-run across
// the main and worktree runs of a multi-worktree session (plan §3):
// local_resource, k8s_yaml, and k8s_resource. The Tiltfile executes once per
// run; scope replaces branching on worktree.name() for resource declarations.
const (
	ScopeAll      = "all"      // default: every run defines it (classic behavior)
	ScopeMain     = "main"     // instantiated only in the main run; worktree runs drop it
	ScopeWorktree = "worktree" // instantiated only in worktree runs; the main run drops it
)

// parseScope validates the authored scope value. Empty means the default
// (ScopeAll).
func parseScope(fnName, v string) (string, error) {
	switch v {
	case "":
		return ScopeAll, nil
	case ScopeAll, ScopeMain, ScopeWorktree:
		return v, nil
	default:
		return "", fmt.Errorf("%s: scope must be one of \"main\", \"worktree\", \"all\"; is %q", fnName, v)
	}
}

// scopeInstantiates reports whether a resource with the given scope is
// instantiated by the run executing for `worktree` ("" for the main run).
func scopeInstantiates(scope, worktree string) bool {
	switch scope {
	case ScopeMain:
		return worktree == ""
	case ScopeWorktree:
		return worktree != ""
	default:
		return true
	}
}

// skipScope logs the instantiation skip for a declaration the current run
// does not name. Shared by the k8s and local_resource scope seams.
func (s *tiltfileState) skipScope(what, name, scope string) {
	run := "main"
	if s.worktree != "" {
		run = "worktree " + s.worktree
	}
	if name == "" {
		s.logger.Verbosef("%s: scope %q does not instantiate in the %s run; skipped",
			what, scope, run)
		return
	}
	s.logger.Verbosef("%s %q: scope %q does not instantiate in the %s run; skipped",
		what, name, scope, run)
}
