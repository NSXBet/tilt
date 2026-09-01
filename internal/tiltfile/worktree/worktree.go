package worktree

import (
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
	"github.com/tilt-dev/tilt/internal/tiltfile/value"
)

// The worktree starlark module: worktree.name(), worktree.dir(),
// worktree.shared(), worktree_config().
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
// (plan §4.3 wtOwned); the main run itself takes none.
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

// worktree.shared(name): true when `name` is defined by the main run —
// a shared resource this worktree inherits rather than clones (plan §3:
// "Shared-hack inheritance"). The gate is the main-run result: the engine's
// re-execution driver evaluates the root Tiltfile for main BEFORE the
// worktree runs and passes the main-defined manifest names through
// WithShared. With no main-run result (main == nil, e.g. the main run
// itself), nothing is shared: worktree.shared returns false and errors are
// left to dep validation (validate.Combine).
func (p Plugin) worktreeShared(t *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	err := starkit.UnpackArgs(t, fn.Name(), args, kwargs, "name", &name)
	if err != nil {
		return nil, err
	}
	return starlark.Bool(p.shared[name]), nil
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
