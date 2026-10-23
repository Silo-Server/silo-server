import { render as renderPage, screen, within } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import LibraryMetadataSettings from "./LibraryMetadataSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

const useSettingsFormMock = vi.fn();
const useRestartKeysMock = vi.fn(() => new Set<string>());

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQueryClient: () => ({}),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => useRestartKeysMock(),
}));

/** A transcode node subtitle sync could run on. */
const transcodeNode = { id: 1, name: "node-1", type: "transcode", enabled: true, healthy: true };
const useAdminNodesMock = vi.fn(() => ({
  data: [transcodeNode] as unknown[] | undefined,
  isSuccess: true,
}));

vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: () => useAdminNodesMock(),
}));

const ratingSourcesMock = vi.fn(() => ({
  isError: false,
  isSuccess: true,
  data: {
    items: [
      { source: "imdb", label: "IMDb", name: "IMDb", always_shown: true },
      { source: "tmdb", label: "TMDB", name: "TMDB", always_shown: true },
      {
        source: "rt_critic",
        label: "Rotten Tomatoes critics",
        name: "RT",
        always_shown: false,
        provider: "MDBList",
      },
      {
        source: "kinopoisk",
        label: "Kinopoisk",
        name: "Kinopoisk",
        always_shown: false,
        provider: "Kinopoisk Metadata",
      },
    ],
  },
}));

const ratingSourceCapabilitiesMock = vi.fn(() => ({
  data: { plugin_declared_sources: true } as { plugin_declared_sources: boolean } | undefined,
}));

vi.mock("@/hooks/queries/admin/ratingSources", () => ({
  useAdminRatingSourceCapabilities: () => ratingSourceCapabilitiesMock(),
  useAdminRatingSources: () => ratingSourcesMock(),
}));

const makeForm = (values: Record<string, string>, dirty: string[] = []) =>
  fakeSettingsForm(values, { dirty });

function render(values: Record<string, string>, dirty: string[] = []) {
  useSettingsFormMock.mockReturnValue(makeForm(values, dirty));
  return renderToStaticMarkup(
    <MemoryRouter>
      <LibraryMetadataSettings />
    </MemoryRouter>,
  );
}

/** Opens one group's Advanced disclosure via its persisted state. */
function expand(id: string) {
  localStorage.setItem(`silo.admin.advanced.${id}`, "true");
}

function text(markup: string): string {
  const container = document.createElement("div");
  container.innerHTML = markup;
  return container.textContent ?? "";
}

// The toggle is a Radix switch, so reach it through the label association
// SettingField sets up rather than by scanning the markup.
function toggleChecked(markup: string, label: string): boolean {
  const container = document.createElement("div");
  container.innerHTML = markup;
  const labelEl = Array.from(container.querySelectorAll("label")).find(
    (el) => el.textContent?.trim() === label,
  );
  if (!labelEl?.htmlFor) throw new Error(`no label found for ${label}`);
  const control = container.querySelector(`[id="${labelEl.htmlFor}"]`);
  if (!control) throw new Error(`no control found for ${label}`);
  return control.getAttribute("aria-checked") === "true";
}

describe("LibraryMetadataSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    useRestartKeysMock.mockReturnValue(new Set<string>());
    useAdminNodesMock.mockReturnValue({ data: [transcodeNode], isSuccess: true });
  });

  it("shows the versions switch as the opposite of the stored scope key", () => {
    const label = "Show all library versions on detail page";
    expect(toggleChecked(render({}), label)).toBe(true);
    expect(toggleChecked(render({ "catalog.scope_versions_to_library": "false" }), label)).toBe(
      true,
    );
    expect(toggleChecked(render({ "catalog.scope_versions_to_library": "true" }), label)).toBe(
      false,
    );
  });

  it("shows the server-wide real-time monitoring switch only while it is off", () => {
    const off = render({ "scanner.realtime_monitoring": "false" });
    expect(toggleChecked(off, "Real-time monitoring")).toBe(false);

    // Turning it back on keeps it in view until the change is saved.
    const turningOn = render({ "scanner.realtime_monitoring": "true" }, [
      "scanner.realtime_monitoring",
    ]);
    expect(toggleChecked(turningOn, "Real-time monitoring")).toBe(true);
  });

  it("lists the ratings plugins declare, grouped by plugin, but not IMDb and TMDB", () => {
    const markup = render({ "catalog.extra_rating_sources": "kinopoisk" });
    const page = text(markup);

    expect(page).toContain("From MDBList");
    expect(page).toContain("Rotten Tomatoes critics");
    expect(page).toContain("From Kinopoisk Metadata");
    expect(page).toContain("Kinopoisk");
    const container = document.createElement("div");
    container.innerHTML = markup;
    const labels = Array.from(container.querySelectorAll("label")).map((l) => l.textContent);
    expect(labels).not.toContain("IMDb");
    expect(labels).not.toContain("TMDB");
  });

  it("lists a turned-on rating that no enabled plugin adds, so it can be turned off", () => {
    const page = text(render({ "catalog.extra_rating_sources": "rt_critic,letterboxd" }));

    expect(page).toContain("Not added by an enabled plugin");
    expect(page).toContain("letterboxd");
    expect(page).not.toContain("No metadata plugin adds ratings.");
  });

  it("leaves out the Ratings group on a server without rating source support", () => {
    ratingSourceCapabilitiesMock.mockReturnValueOnce({ data: undefined });
    const page = text(render({ "catalog.extra_rating_sources": "kinopoisk" }));

    expect(page).not.toContain("Metadata plugins can add other ratings");
    expect(page).not.toContain("From Kinopoisk Metadata");
  });
});

describe("LibraryMetadataSettings subtitle sync", () => {
  beforeEach(() => {
    localStorage.clear();
    useRestartKeysMock.mockReturnValue(new Set<string>());
    useAdminNodesMock.mockReturnValue({ data: [transcodeNode], isSuccess: true });
  });

  function renderInteractive(values: Record<string, string>, dirty: string[] = []) {
    const form = makeForm(values, dirty);
    useSettingsFormMock.mockReturnValue(form);
    const view = renderPage(
      <MemoryRouter>
        <LibraryMetadataSettings />
      </MemoryRouter>,
    );
    return {
      form,
      unmount: view.unmount,
      group: within(screen.getByRole("group", { name: "Subtitle sync" })),
    };
  }

  it("hides node capacity for local sync and warns about node-only sync without nodes", () => {
    expand("library.subtitle_sync");
    const local = renderInteractive({ "subtitles.sync_execution": "local" });
    expect(local.group.getByRole("button", { name: "Advanced · 1 setting" })).toBeInTheDocument();
    expect(
      local.group.queryByRole("spinbutton", { name: /Concurrent syncs per transcode node/ }),
    ).not.toBeInTheDocument();
    local.unmount();

    useAdminNodesMock.mockReturnValue({ data: [], isSuccess: true });
    const nodeOnly = renderInteractive({ "subtitles.sync_execution": "transcode_nodes_only" });
    expect(nodeOnly.group.getByText("No transcode nodes are connected")).toBeInTheDocument();
  });

  it("keeps an unsaved node capacity in view after sync moves to the local server", () => {
    expand("library.subtitle_sync");
    const { group } = renderInteractive({ "subtitles.sync_execution": "local" }, [
      "subtitles.sync_execution",
      "subtitles.sync_node_capacity",
    ]);

    expect(group.getByRole("button", { name: "Advanced · 2 settings" })).toBeInTheDocument();
    expect(
      group.getByRole("spinbutton", { name: /Concurrent syncs per transcode node/ }),
    ).toBeVisible();
  });

  it("does not warn before the node list has loaded", () => {
    expand("library.subtitle_sync");
    useAdminNodesMock.mockReturnValue({ data: undefined, isSuccess: false });
    const { group } = renderInteractive({ "subtitles.sync_execution": "transcode_nodes_only" });

    expect(group.queryByText("No transcode nodes are connected")).not.toBeInTheDocument();
  });
});
