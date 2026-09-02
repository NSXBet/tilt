package worktree

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/pkg/model"
)

func TestSplitName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected model.ManifestName
	}{
		{"", ""},
		{"postgres", "postgres"},
		{"(Tiltfile)", "(Tiltfile)"},
		{"wt:feat-auth/incidents-admin", "incidents-admin"},
		{"wt:feat-auth/api", "api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wt, base := SplitName(model.ManifestName(tc.name))
			assert.Equal(t, tc.expected, base)
			if tc.expected == model.ManifestName(tc.name) {
				// unprefixed names pass through untouched
				assert.Equal(t, model.ManifestName(""), wt)
			} else {
				assert.Equal(t, model.ManifestName("feat-auth"), wt)
			}
		})
	}

	// A malformed clone-ish name without the separator is passed through
	// unchanged: the pass does not invent structure it did not write.
	wt, base := SplitName("wt:noseparator")
	require.Equal(t, model.ManifestName("wt:noseparator"), base)
	require.Equal(t, model.ManifestName(""), wt)
}

func TestParseTiltfileName(t *testing.T) {
	wt, ok := ParseTiltfileName("tiltfile:feat-auth")
	require.True(t, ok)
	assert.Equal(t, "feat-auth", wt)

	_, ok = ParseTiltfileName(model.MainTiltfileManifestName)
	assert.False(t, ok)

	_, ok = ParseTiltfileName("postgres")
	assert.False(t, ok)

	_, ok = ParseTiltfileName("wt:feat-auth/api")
	assert.False(t, ok)
}
