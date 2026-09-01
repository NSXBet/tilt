package worktree

import (
	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
)

func MustState(model starkit.Model) State {
	state, err := GetState(model)
	if err != nil {
		panic(err)
	}
	return state
}

func GetState(m starkit.Model) (State, error) {
	var state State
	err := m.Load(&state)
	return state, err
}
