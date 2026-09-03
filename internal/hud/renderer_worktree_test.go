package hud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/hud/view"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

func TestNextWorktreeFilter(t *testing.T) {
	resources := []view.Resource{
		{Name: "postgres"},
		{Name: "wt:feat-auth_api", Worktree: "feat-auth"},
		{Name: "wt:feat-auth_frontend", Worktree: "feat-auth"},
		{Name: "wt:fix-bug_api", Worktree: "fix-bug"},
	}

	next, ok := nextWorktreeFilter(resources, "")
	require.True(t, ok)
	assert.Equal(t, "feat-auth", next)

	next, ok = nextWorktreeFilter(resources, "feat-auth")
	require.True(t, ok)
	assert.Equal(t, "fix-bug", next)

	// cycling wraps back to "all"
	next, ok = nextWorktreeFilter(resources, "fix-bug")
	require.True(t, ok)
	assert.Equal(t, "", next)

	// no worktrees: cycling is a no-op
	next, ok = nextWorktreeFilter([]view.Resource{{Name: "postgres"}}, "")
	assert.False(t, ok)
	assert.Equal(t, "", next)
}

func TestFilterResourcesByWorktree(t *testing.T) {
	main := view.Resource{Name: "postgres"}
	wtA := view.Resource{Name: "wt:feat-auth_api", Worktree: "feat-auth"}
	wtB := view.Resource{Name: "wt:fix-bug_api", Worktree: "fix-bug"}
	all := []view.Resource{main, wtA, wtB}

	assert.Equal(t, all, filterResourcesByWorktree(all, ""))
	assert.Equal(t, []view.Resource{wtA}, filterResourcesByWorktree(all, "feat-auth"))
	// The filter selects one worktree's clones; shared main-checkout
	// resources have no worktree and drop out while filtering.
	assert.Equal(t, []view.Resource{wtB}, filterResourcesByWorktree(all, "fix-bug"))
	assert.Empty(t, filterResourcesByWorktree(all, "no-such-worktree"))
}

func TestResourceRowsSingleWorktreeUnchanged(t *testing.T) {
	// The common single-checkout case must render exactly as before: no
	// group headers, one scroll child per resource.
	rs := []view.Resource{
		{Name: "postgres"},
		{Name: "api"},
	}
	names, rows := resourceRows(rs)
	assert.Equal(t, []string{"postgres", "api"}, names)
	require.Len(t, rows, 2)
	assert.Equal(t, 0, rows[0].resourceIndex)
	assert.Equal(t, 1, rows[1].resourceIndex)
}

func TestResourceRowsGroupedByWorktree(t *testing.T) {
	rs := []view.Resource{
		{Name: "wt:feat-auth_api", Worktree: "feat-auth"},
		{Name: "postgres"},
		{Name: "wt:fix-bug_api", Worktree: "fix-bug"},
		{Name: "wt:feat-auth_frontend", Worktree: "feat-auth"},
	}

	names, rows := resourceRows(rs)

	// main checkout's shared resources sort last; worktrees alphabetical;
	// one header per worktree, registered as a scroll child under a name
	// clones can't collide with (`wt:<wt>_` has no bare base segment).
	expected := []string{
		"wt:feat-auth_",
		"wt:feat-auth_api",
		"wt:feat-auth_frontend",
		"wt:fix-bug_",
		"wt:fix-bug_api",
		"postgres",
	}
	assert.Equal(t, expected, names)

	require.Len(t, rows, 6)
	assert.True(t, rows[0].isGroupHeader)
	assert.Equal(t, "feat-auth", rows[0].worktree)
	assert.Equal(t, -1, rows[0].resourceIndex)
	assert.Equal(t, 0, rows[1].resourceIndex)
	assert.Equal(t, 3, rows[2].resourceIndex)
	assert.True(t, rows[3].isGroupHeader)
	assert.Equal(t, "fix-bug", rows[3].worktree)
	assert.Equal(t, 2, rows[4].resourceIndex)
	assert.Equal(t, 1, rows[5].resourceIndex)
}

// The worktree grouping renders: one header per worktree, the worktree name
// on each clone row, shared main-checkout resources ungrouped at the bottom.
func TestRenderWorktreeGroups(t *testing.T) {
	rtf := newRendererTestFixture(t)

	v := newView(
		view.Resource{
			Name:     "postgres",
			Worktree: "",
			ResourceInfo: view.K8sResourceInfo{
				PodStatus: "Running",
				RunStatus: v1alpha1.RuntimeStatusOK,
			},
		},
		view.Resource{
			Name:     "wt:feat-auth_api",
			Worktree: "feat-auth",
			ResourceInfo: view.K8sResourceInfo{
				PodStatus: "Running",
				RunStatus: v1alpha1.RuntimeStatusOK,
			},
		},
		view.Resource{
			Name:     "wt:fix-bug_api",
			Worktree: "fix-bug",
			ResourceInfo: view.K8sResourceInfo{
				PodStatus: "Running",
				RunStatus: v1alpha1.RuntimeStatusOK,
			},
		},
	)
	v.LogReader = newLogReader("")

	vs := fakeViewState(3, view.CollapseAuto)
	rtf.run("worktree groups", 80, 20, v, vs)

	// filtering to one worktree hides the others and their group headers
	vs.WorktreeFilter = "feat-auth"
	vs.Resources = fakeViewState(1, view.CollapseAuto).Resources
	rtf.run("worktree filter feat-auth", 80, 20, v, vs)
}

func TestRenderWorktreeNameInRow(t *testing.T) {
	rtf := newRendererTestFixture(t)

	v := newView(view.Resource{
		Name:     "wt:feat-auth_api",
		Worktree: "feat-auth",
		ResourceInfo: view.K8sResourceInfo{
			PodStatus: "Running",
			RunStatus: v1alpha1.RuntimeStatusOK,
		},
	})
	v.LogReader = newLogReader("")

	vs := fakeViewState(1, view.CollapseAuto)
	rtf.run("worktree name in row", 80, 8, v, vs)
}
