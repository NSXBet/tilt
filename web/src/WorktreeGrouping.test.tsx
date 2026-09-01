import { render, screen, within } from "@testing-library/react"
import { SnackbarProvider } from "notistack"
import React from "react"
import { MemoryRouter } from "react-router"
import Features, { FeaturesTestProvider, Flag } from "./feature"
import { GroupByLabelView } from "./labels"
import LogStore from "./LogStore"
import OverviewTable, {
  labeledResourcesToTableCells,
  OverviewGroup,
  OverviewGroupName,
} from "./OverviewTable"
import { Name, RowValues } from "./OverviewTableColumns"
import PathBuilder from "./PathBuilder"
import { ResourceGroupsContextProvider } from "./ResourceGroupsContext"
import {
  DEFAULT_OPTIONS,
  ResourceListOptions,
  ResourceListOptionsProvider,
} from "./ResourceListOptionsContext"
import { matchesResourceName } from "./ResourceNameFilter"
import SidebarItem from "./SidebarItem"
import SidebarResources from "./SidebarResources"
import { ResourceSelectionProvider } from "./ResourceSelectionContext"
import { oneResource, TestDataView } from "./testdata"
import { ResourceView, UIResource } from "./types"
// Mirrors the module-local `runningTiltBuild` fixture in testdata.tsx.
const runningTiltBuild = {
  commitSHA: "658f2719f3380bee8e7119c7eb29f4a4a986ac6e",
  date: "2020-12-10",
  dev: true,
  version: "0.17.13",
}

// The plan pins the worktree marker to the `tilt.dev/worktree` label that the
// engine stamps on every UIResource (Manifest labels flow through
// internal/hud/webview/convert.go:266 to UIResource metadata.labels), while
// engine-internal resource names carry the worktree prefix (plan §4.3:
// "feat-auth/incidents-admin").
const WORKTREE_LABEL_KEY = "tilt.dev/worktree"

// Group heading shown for each worktree section (table + sidebar).
const wtGroupLabel = (worktree: string) => `worktrees: ${worktree}`

let pathBuilder = PathBuilder.forTesting("localhost", "/")

function worktreeResource(opts: {
  name: string
  worktree?: string
  order?: number
}): UIResource {
  const res = oneResource({
    // Engine-internal names are prefixed (internal/tiltfile/worktree/prefix.go:
    // `wt:<worktree>/<name>`); the UI sees the prefixed name.
    name: opts.worktree ? `wt:${opts.worktree}/${opts.name}` : opts.name,
    labels: 0,
    order: opts.order,
  })
  if (opts.worktree) {
    res.metadata!.labels = { [WORKTREE_LABEL_KEY]: opts.worktree }
  }
  return res
}

function worktreeView(resources: UIResource[]): TestDataView {
  return {
    uiResources: resources,
    uiButtons: [],
    uiSession: { status: { tiltfileKey: "test", runningTiltBuild } },
  }
}

function tableViewWithSettings({
  view,
  resourceListOptions,
}: {
  view: TestDataView
  resourceListOptions?: Partial<ResourceListOptions>
}) {
  const features = new Features({ [Flag.Labels]: true })
  return (
    <MemoryRouter
      initialEntries={["/"]}
      future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
    >
      <SnackbarProvider>
        <FeaturesTestProvider value={features}>
          <ResourceGroupsContextProvider>
            <ResourceListOptionsProvider
              initialValuesForTesting={{
                ...DEFAULT_OPTIONS,
                ...resourceListOptions,
              }}
            >
              <ResourceSelectionProvider initialValuesForTesting={[]}>
                <OverviewTable view={view} />
              </ResourceSelectionProvider>
            </ResourceListOptionsProvider>
          </ResourceGroupsContextProvider>
        </FeaturesTestProvider>
      </SnackbarProvider>
    </MemoryRouter>
  )
}

function renderTable(
  view: TestDataView,
  options?: Partial<ResourceListOptions>
) {
  return render(tableViewWithSettings({ view, resourceListOptions: options }))
    .container
}

afterEach(() => {
  sessionStorage.clear()
  localStorage.clear()
})

describe("OverviewTable worktree grouping", () => {
  it("renders a group section per worktree with its resources", () => {
    const container = renderTable(
      worktreeView([
        worktreeResource({
          name: "frontend",
          worktree: "feat-auth",
          order: 2,
        }),
        worktreeResource({ name: "api", order: 1 }),
      ])
    )

    const worktreeGroup = Array.from(
      container.querySelectorAll(OverviewGroup)
    ).find(
      (g) =>
        g.querySelector(OverviewGroupName)?.textContent ===
        wtGroupLabel("feat-auth")
    )
    expect(worktreeGroup).toBeDefined()

    const names = Array.from(worktreeGroup!.querySelectorAll(Name)).map(
      (n) => n.querySelector("span")?.textContent ?? n.textContent
    )
    expect(names).toEqual(["wt:feat-auth/frontend"])
  })

  it("groups resources by worktree in sorted order in the cell model", () => {
    const view = worktreeView([
      worktreeResource({ name: "frontend", worktree: "wt-b", order: 1 }),
      worktreeResource({ name: "api", worktree: "wt-a", order: 2 }),
    ])
    const cells: GroupByLabelView<RowValues> = labeledResourcesToTableCells(
      view.uiResources,
      view.uiButtons,
      new LogStore()
    )

    const wtLabels = cells.labels.filter((l) => l.startsWith("worktrees: "))
    expect(wtLabels).toEqual([wtGroupLabel("wt-a"), wtGroupLabel("wt-b")])
    expect(cells.labelsToResources[wtGroupLabel("wt-a")].map((r) => r.name)) //
      .toEqual(["wt:wt-a/api"])
    expect(cells.labelsToResources[wtGroupLabel("wt-b")].map((r) => r.name)) //
      .toEqual(["wt:wt-b/frontend"])
  })

  it("keeps main-run resources in the unlabeled bucket, not a worktree group", () => {
    const view = worktreeView([
      worktreeResource({ name: "api", order: 1 }),
      worktreeResource({ name: "frontend", worktree: "feat-auth", order: 2 }),
    ])
    const cells = labeledResourcesToTableCells(
      view.uiResources,
      view.uiButtons,
      new LogStore()
    )
    // tilt.dev/worktree is a prefixed label, so it must not be treated as a
    // user label; the main-run resource lands in the unlabeled bucket
    expect(cells.unlabeled.map((r) => r.name)).toEqual(["api"])
  })

  it("does not render a worktree group when no resource has the worktree label", () => {
    const container = renderTable(
      worktreeView([worktreeResource({ name: "api", order: 1 })])
    )
    const groupNames = Array.from(
      container.querySelectorAll(OverviewGroupName)
    ).map((g) => g.textContent)
    expect(groupNames).not.toContain(wtGroupLabel("feat-auth"))
  })
})

describe("OverviewTableColumns worktree badge", () => {
  it("shows the worktree name as a badge on worktree resource rows", () => {
    const container = renderTable(
      worktreeView([
        worktreeResource({ name: "frontend", worktree: "feat-auth" }),
      ])
    )
    const row = Array.from(container.querySelectorAll("tr")).find((tr) =>
      tr.textContent?.includes("frontend")
    )
    expect(row).toBeDefined()
    // the badge renders the bare worktree name (distinct from the prefixed
    // resource name "wt:feat-auth/frontend")
    expect(within(row!).getByText("feat-auth")).toBeInTheDocument()
  })

  it("does not render a worktree badge on main-run resource rows", () => {
    const container = renderTable(
      worktreeView([
        worktreeResource({ name: "frontend", worktree: "feat-auth", order: 1 }),
        worktreeResource({ name: "api", order: 2 }),
      ])
    )
    const row = Array.from(container.querySelectorAll("tr")).find((tr) =>
      tr.textContent?.includes("api")
    )
    expect(row).toBeDefined()
    expect(within(row!).queryByText("feat-auth")).toBeNull()
  })
})

describe("SidebarResources worktree sections", () => {
  function renderSidebar(view: TestDataView) {
    const logStore = new LogStore()
    const items = view.uiResources.map((r) => new SidebarItem(r, logStore))
    const features = new Features({ [Flag.Labels]: true })
    return render(
      <MemoryRouter
        initialEntries={["/"]}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
      >
        <FeaturesTestProvider value={features}>
          <ResourceGroupsContextProvider>
            <SidebarResources
              items={items}
              selected=""
              resourceView={ResourceView.Log}
              pathBuilder={pathBuilder}
              resourceListOptions={DEFAULT_OPTIONS}
            />
          </ResourceGroupsContextProvider>
        </FeaturesTestProvider>
      </MemoryRouter>
    ).container
  }

  it("renders a section per worktree holding only that worktree's resources", () => {
    const container = renderSidebar(
      worktreeView([
        worktreeResource({ name: "frontend", worktree: "feat-auth" }),
        worktreeResource({ name: "backend", worktree: "feat-auth", order: 2 }),
        worktreeResource({ name: "api", order: 3 }),
      ])
    )

    const headings = Array.from(container.querySelectorAll("span")).map(
      (s) => s.textContent
    )
    expect(headings).toContain(wtGroupLabel("feat-auth"))
    expect(screen.getByText("wt:feat-auth/frontend")).toBeInTheDocument()
    expect(screen.getByText("wt:feat-auth/backend")).toBeInTheDocument()
    expect(screen.getByText("api")).toBeInTheDocument()
  })

  it("does not render a worktree section when no resource is in a worktree", () => {
    const container = renderSidebar(
      worktreeView([worktreeResource({ name: "api", order: 1 })])
    )
    const headings = Array.from(container.querySelectorAll("span")).map(
      (s) => s.textContent
    )
    expect(headings).not.toContain(wtGroupLabel("feat-auth"))
  })
})

describe("ResourceNameFilter worktree matching", () => {
  // Display names carry the worktree prefix (plan §4.3), so filtering by the
  // worktree token must match them; main resources stay hidden.
  it("matches prefixed worktree display names", () => {
    expect(
      matchesResourceName("wt:feat-auth/incidents-admin", "feat-auth")
    ).toBe(true)
    expect(
      matchesResourceName("wt:feat-auth/incidents-admin", "incidents-admin")
    ).toBe(true)
    expect(matchesResourceName("postgres", "feat-auth")).toBe(false)
  })

  it("filters the table to the named worktree's resources", () => {
    const container = renderTable(
      worktreeView([
        worktreeResource({ name: "frontend", worktree: "feat-auth", order: 1 }),
        worktreeResource({ name: "api", order: 2 }),
      ]),
      { resourceNameFilter: "feat-auth" }
    )
    const names = Array.from(container.querySelectorAll(Name)).map(
      (n) => n.querySelector("span")?.textContent ?? n.textContent
    )
    expect(names).toEqual(["wt:feat-auth/frontend"])
  })
})
