package starkit

import (
	"path/filepath"

	"go.starlark.net/starlark"
)

// We want to resolve paths relative to the dir where the currently executing file lives,
// not relative to the working directory.
func AbsPath(t *starlark.Thread, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(AbsWorkingDir(t), path)
}

func AbsWorkingDir(t *starlark.Thread) string {
	if worktree, ok := t.Local(worktreeContextKey).(WorktreeContext); ok && worktree.Dir != "" {
		return worktree.Dir
	}
	return filepath.Dir(CurrentExecPath(t))
}

// Path to the file that's currently executing
func CurrentExecPath(t *starlark.Thread) string {
	ret := t.Local(execingTiltfileKey)
	if ret == nil {
		panic("internal error: currentExecPath must be called from an active starlark thread")
	}
	return ret.(string)
}

// WorktreeContextOf returns the worktree context injected into this thread
// by the plugin's OnStart (plan §0): Name is "" in the main run, the
// worktree name in a re-execution. Builtins that must vary per run but take
// no worktree argument (e.g. docker_compose project naming, plan §4.5) read
// the run's context from here instead of threading it through starlark.
func WorktreeContextOf(t *starlark.Thread) WorktreeContext {
	if worktree, ok := t.Local(worktreeContextKey).(WorktreeContext); ok {
		return worktree
	}
	return WorktreeContext{}
}
