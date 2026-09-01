// Package portregistry allocates local ports for worktree resources.
//
// Allocate(owner, requested) honors a free requested port, falls back to the
// OS (:0) when requested is 0, stays stable for the same owner across Tiltfile
// reloads, and errors when the worktree port_range is exhausted. See
// .agents/drafts/multi-worktree-parallel.md §5.
package portregistry
