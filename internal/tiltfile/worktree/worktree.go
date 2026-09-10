package worktree

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"go.starlark.net/starlark"

	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
	"github.com/tilt-dev/tilt/internal/tiltfile/value"
)

// The worktree starlark module: worktree.name(), worktree.dir(),
// worktree.branch(), worktree.eq(), worktree.shared(), worktree_config().
//
// The same root Tiltfile is re-executed per worktree with a worktree context
// injected through this plugin's construction (plan §0: the engine's
// re-execution driver builds the per-run Environment, so the context cannot
// leak across worktree runs). In the main run there is no context:
// worktree.name() returns "" — classic behavior preserved.
type Plugin struct {
	// worktree context for this run; empty for the main run.
	name string
	dir  string
	// main-run-defined (shared) manifest names, supplied by the engine's
	// re-execution driver for worktree runs; empty for the main run.
	shared map[string]bool
}

// Option configures the worktree plugin.
type Option func(*Plugin)

// WithWorktree carries the worktree context for this run, supplied by the
// engine's re-execution driver (plan §7.1):
// NewPlugin(WithWorktree(name, dir)).
func WithWorktree(name, dir string) Option {
	return func(p *Plugin) {
		p.name = name
		p.dir = dir
	}
}

// WithShared supplies the main-run-defined (shared) manifest names for
// worktree runs, gating worktree.shared(name). The engine's re-execution
// driver derives the set from the main run's TiltfileLoadResult manifests
// (plan §4.3 wtOwned); the main run itself takes none — a set supplied
// without WithWorktree is ignored (worktree.shared stays False).
func WithShared(names []string) Option {
	return func(p *Plugin) {
		p.shared = make(map[string]bool, len(names))
		for _, n := range names {
			p.shared[n] = true
		}
	}
}

func NewPlugin(opts ...Option) Plugin {
	p := Plugin{}
	for _, opt := range opts {
		opt(&p)
	}
	return p
}

func (p Plugin) OnStart(env *starkit.Environment) error {
	env.SetWorktreeContext(starkit.WorktreeContext{Name: p.name, Dir: p.dir})
	err := env.AddBuiltin("worktree.name", p.worktreeName)
	if err != nil {
		return err
	}
	err = env.AddBuiltin("worktree.dir", p.worktreeDir)
	if err != nil {
		return err
	}
	err = env.AddBuiltin("worktree.shared", p.worktreeShared)
	if err != nil {
		return err
	}
	err = env.AddBuiltin("worktree.branch", p.worktreeBranch)
	if err != nil {
		return err
	}
	err = env.AddBuiltin("worktree.eq", p.worktreeEq)
	if err != nil {
		return err
	}
	return env.AddBuiltin("worktree_config", worktreeConfig)
}

func (p Plugin) NewState() interface{} {
	return State{
		Dir:      DefaultDir,
		Gateway:  true,
		PortMin:  0,
		PortMax:  0,
		Worktree: p.name,
	}
}

var _ starkit.StatefulPlugin = Plugin{}

// State accumulated during Tiltfile execution. WorktreeConfig feeds
// TiltfileLoadResult.WorktreeConfig (plan §7.1).
type State struct {
	// Overrides from worktree_config().
	Dir     string
	PortMin int
	PortMax int
	Gateway bool

	// The worktree this run executes for; "" for the main run.
	Worktree string
}

// worktree.name(): the name of the worktree this run executes for
// ("" in the main run).
func (p Plugin) worktreeName(t *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	err := starkit.UnpackArgs(t, fn.Name(), args, kwargs)
	if err != nil {
		return nil, err
	}
	return starlark.String(p.name), nil
}

// worktree.dir(): the directory of the worktree this run executes for
// ("" in the main run). For worktree runs the engine's re-execution driver
// re-roots path resolution (AbsWorkingDir) at the worktree (plan §0
// amendment), so docker_build(".")/sync()/local() read worktree files with
// zero path edits; this builtin reports that root explicitly.
func (p Plugin) worktreeDir(t *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	err := starkit.UnpackArgs(t, fn.Name(), args, kwargs)
	if err != nil {
		return nil, err
	}
	return starlark.String(p.dir), nil
}

// worktree.branch(): the git branch checked out in the checkout this run
// executes for ("" on a detached HEAD). The main run resolves the main
// checkout from the executing Tiltfile's position; a worktree run resolves
// the injected worktree dir. A checkout outside git errors: a Tiltfile
// branching on branches cannot run there silently.
func (p Plugin) worktreeBranch(t *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	err := starkit.UnpackArgs(t, fn.Name(), args, kwargs)
	if err != nil {
		return nil, err
	}
	branch, err := p.branchOf(t)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", fn.Name(), err)
	}
	return starlark.String(branch), nil
}

// worktree.eq(branch): true when the checkout this run executes for has
// `branch` checked out — the branch-based form of branching on
// worktree.name() for worktrees that mirror their branch names. The main
// run compares the MAIN checkout's branch, so `worktree.eq("main")` is the
// shared-foundation test. Same failure behavior as worktree.branch(): a
// checkout outside git errors; a detached HEAD matches only "".
func (p Plugin) worktreeEq(t *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var branch string
	err := starkit.UnpackArgs(t, fn.Name(), args, kwargs, "branch", &branch)
	if err != nil {
		return nil, err
	}
	current, err := p.branchOf(t)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", fn.Name(), err)
	}
	return starlark.Bool(current == branch), nil
}

// branchOf resolves the git branch of the checkout this run executes for:
// the injected worktree dir, or the executing Tiltfile's directory for the
// main run (its position is the main checkout).
func (p Plugin) branchOf(t *starlark.Thread) (string, error) {
	dir := p.dir
	if dir == "" {
		dir = filepath.Dir(starkit.CurrentExecPath(t))
	}
	return gitBranch(dir)
}

// gitBranch returns the branch checked out in dir via
// `git branch --show-current` ("" on a detached HEAD). It shells out to
// porcelain instead of reading HEAD: a linked worktree's HEAD lives in the
// repo's worktrees/ metadata, and the porcelain contract is stable.
func gitBranch(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "branch", "--show-current").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolving the checked-out branch of %q: %v: %s",
			dir, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// worktree.shared(name): true when `name` is defined by the main run —
// a shared resource this worktree inherits rather than clones (plan §3:
// "Shared-hack inheritance"). The gate is the main-run result: the engine's
// re-execution driver evaluates the root Tiltfile for main BEFORE the
// worktree runs and passes the main-defined manifest names through
// WithShared. The main run itself never shares (name() == "" ⇒ False even
// if a set was supplied): nothing is shared with itself, and classic
// main-run behavior cannot break on a miswired driver. With no main-run
// result (shared set empty, e.g. main == nil in Combine), nothing is
// shared: worktree.shared returns false and errors are left to dep
// validation (validate.Combine). An empty name errors: it can never be a
// real resource.
func (p Plugin) worktreeShared(t *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	err := starkit.UnpackArgs(t, fn.Name(), args, kwargs, "name", &name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("%s: name must be non-empty", fn.Name())
	}
	return starlark.Bool(p.name != "" && p.shared[name]), nil
}

// worktree_config: the main Tiltfile's optional overrides (plan §3).
// port_range unset → (0, 0): fall back to OS :0 allocation (plan §5).
func worktreeConfig(t *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var dir value.Stringable
	var portRangeVal starlark.Value = starlark.None
	var gatewayVal starlark.Value = starlark.None

	err := starkit.UnpackArgs(t, fn.Name(), args, kwargs,
		"dir?", &dir,
		"port_range?", &portRangeVal,
		"gateway?", &gatewayVal,
	)
	if err != nil {
		return nil, err
	}

	portMin, portMax, err := unpackPortRange(portRangeVal)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", fn.Name(), err)
	}

	gateway := true
	if gatewayVal != nil && gatewayVal != starlark.None {
		b, ok := gatewayVal.(starlark.Bool)
		if !ok {
			return nil, fmt.Errorf("%s: gateway must be a bool; is a %T", fn.Name(), gatewayVal)
		}
		gateway = bool(b)
	}

	return starlark.None, starkit.SetState(t, func(s State) State {
		if dir.Value != "" {
			s.Dir = dir.Value
		}
		if portMin != 0 || portMax != 0 {
			s.PortMin = portMin
			s.PortMax = portMax
		}
		if gatewayVal != starlark.None {
			s.Gateway = gateway
		}
		return s
	})
}

// unpackPortRange parses a (min, max) tuple of ints; None means unset.
func unpackPortRange(val starlark.Value) (int, int, error) {
	if val == nil || val == starlark.None {
		return 0, 0, nil
	}

	seq, ok := val.(starlark.Sequence)
	if !ok {
		return 0, 0, fmt.Errorf("port_range must be a (min, max) tuple of ints; is a %T", val)
	}

	var vals [2]int
	i := 0
	it := seq.Iterate()
	defer it.Done()
	var elem starlark.Value
	for it.Next(&elem) {
		if i >= 2 {
			return 0, 0, fmt.Errorf("port_range must be a (min, max) tuple of 2 ints; got %s", seq.String())
		}
		n, ok := elem.(starlark.Int)
		if !ok {
			return 0, 0, fmt.Errorf("port_range must be a (min, max) tuple of ints; element %d is a %T", i, elem)
		}
		v, ok := n.Int64()
		if !ok {
			return 0, 0, fmt.Errorf("port_range element %d overflows int64", i)
		}
		vals[i] = int(v)
		i++
	}
	if i != 2 {
		return 0, 0, fmt.Errorf("port_range must be a (min, max) tuple of 2 ints; got %d element(s)", i)
	}
	return vals[0], vals[1], nil
}
