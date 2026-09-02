package hud

import (
	"fmt"

	"github.com/gdamore/tcell"

	"github.com/tilt-dev/tilt/internal/hud/view"
	"github.com/tilt-dev/tilt/internal/rty"
	"github.com/tilt-dev/tilt/pkg/model/logstore"
)

type TabView struct {
	view      view.View
	viewState view.ViewState
	tabState  view.TabState
}

func NewTabView(v view.View, vState view.ViewState) *TabView {
	return &TabView{
		view:      v,
		viewState: vState,
		tabState:  vState.TabState,
	}
}

func (v *TabView) Build() rty.Component {
	l := rty.NewConcatLayout(rty.DirVert)
	l.Add(v.buildTabs(false))

	log := rty.NewTextScrollLayout("log")
	log.Add(rty.TextString(v.log()))
	l.Add(log)

	return l
}

func (v *TabView) log() string {
	var numLinesNeeded = logLineCount
	if v.viewState.TiltLogState == view.TiltLogShort {
		numLinesNeeded = defaultLogPaneHeight
	}

	var spanID logstore.SpanID
	switch v.tabState {
	case view.TabBuildLog:
		_, resource := selectedResource(v.view, v.viewState)
		if !resource.CurrentBuild.Empty() {
			spanID = resource.CurrentBuild.SpanID
		} else {
			spanID = resource.LastBuild().SpanID
		}
	case view.TabRuntimeLog:
		_, resource := selectedResource(v.view, v.viewState)
		if resource.ResourceInfo != nil {
			spanID = resource.ResourceInfo.RuntimeSpanID()
		}
	}

	reader := v.view.LogReader
	result := ""
	if v.tabState == view.TabAllLog {
		result = reader.Tail(numLinesNeeded)
	} else if spanID != "" {
		result = reader.TailSpan(numLinesNeeded, spanID)
	}

	if result == "" {
		return "(no logs received)"
	}
	return result
}

func (v *TabView) buildTab(text string) rty.Component {
	return rty.TextString(fmt.Sprintf(" %s ", text))
}

func (v *TabView) buildTabs(isMax bool) rty.Component {
	l := rty.NewLine()
	if v.tabState == view.TabAllLog {
		l.Add(v.buildTab("1: ALL LOGS"))
	} else {
		l.Add(v.buildTab("1: all logs"))
	}
	l.Add(rty.TextString("│"))
	if v.tabState == view.TabBuildLog {
		l.Add(v.buildTab("2: BUILD LOG"))
	} else {
		l.Add(v.buildTab("2: build log"))
	}
	l.Add(rty.TextString("│"))
	if v.tabState == view.TabRuntimeLog {
		l.Add(v.buildTab("3: RUNTIME LOG"))
	} else {
		l.Add(v.buildTab("3: runtime log"))
	}
	wtTabs := v.buildWorktreeTabs()
	if wtTabs != nil {
		l.Add(rty.TextString("│"))
		l.Add(wtTabs)
	}
	l.Add(rty.TextString("│ "))
	l.Add(renderPaneHeader(isMax))
	result := rty.Bg(l, tcell.ColorWhiteSmoke)
	result = rty.Fg(result, cText)
	return result
}

// buildWorktreeTabs appends one filter tab per worktree (plan §9): "all"
// plus each worktree name, with the active one highlighted. No worktree in
// the view means no tabs — the classic single-checkout UI is unchanged.
func (v *TabView) buildWorktreeTabs() rty.Component {
	wts := v.worktrees()
	if len(wts) == 0 {
		return nil
	}
	l := rty.NewLine()
	l.Add(rty.TextString(" "))
	l.Add(v.buildTab("4: all worktrees"))
	for _, wt := range wts {
		l.Add(rty.TextString("│"))
		l.Add(v.buildTab("4: " + wt))
	}
	return l
}

// worktrees returns the distinct worktrees present in the view, in stable
// first-seen order.
func (v *TabView) worktrees() []string {
	seen := make(map[string]bool)
	var out []string
	for _, res := range v.view.Resources {
		if res.Worktree == "" || seen[res.Worktree] {
			continue
		}
		seen[res.Worktree] = true
		out = append(out, res.Worktree)
	}
	return out
}

// nextWorktreeFilter cycles the worktree filter (plan §9): all → each
// worktree in view order → all. Returns false when there is nothing to
// cycle (no worktrees).
func nextWorktreeFilter(resources []view.Resource, current string) (string, bool) {
	seen := make(map[string]bool)
	var wts []string
	for _, res := range resources {
		if res.Worktree == "" || seen[res.Worktree] {
			continue
		}
		seen[res.Worktree] = true
		wts = append(wts, res.Worktree)
	}
	if len(wts) == 0 {
		return "", false
	}
	if current == "" {
		return wts[0], true
	}
	for i, wt := range wts {
		if wt == current {
			if i+1 < len(wts) {
				return wts[i+1], true
			}
			return "", true
		}
	}
	return "", true
}
