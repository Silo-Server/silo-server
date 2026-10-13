import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import LibraryMetadataSettings from "./LibraryMetadataSettings";

const useSettingsFormMock = vi.fn();
const useRestartKeysMock = vi.fn(() => new Set<string>());
const storageAvailableMock = vi.fn(() => true);
const markerCapabilitiesMock = vi.fn<() => { data?: Record<string, boolean> }>(() => ({
  data: { detection_kind_settings: true },
}));

vi.mock("@/hooks/useBranding", () => ({
  useBranding: () => ({ storageAvailable: storageAvailableMock() }),
}));

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

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCatalogSearchStatus: () => ({ data: undefined, isLoading: true }),
}));

vi.mock("@/hooks/queries/admin/markers", () => ({
  useAdminMarkerCapabilities: () => markerCapabilitiesMock(),
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

vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTasks: () => ({ data: [] }),
  useRunTask: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@/components/realtimeEventsContext", () => ({
  useEventChannel: () => undefined,
}));

function makeForm(values: Record<string, string>, dirty: string[] = []) {
  const dirtySet = new Set(dirty);
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    getPersistedValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    resetValue: vi.fn(),
    isDirty: (key: string) => dirtySet.has(key),
    isClearStaged: (key: string) => dirtySet.has(key) && (values[key] ?? "") === "",
    dirtyCount: dirtySet.size,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [] as string[],
    buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
  };
}

function render(values: Record<string, string>, dirty: string[] = []) {
  useSettingsFormMock.mockReturnValue(makeForm(values, dirty));
  return renderToStaticMarkup(
    <MemoryRouter>
      <LibraryMetadataSettings />
    </MemoryRouter>,
  );
}

function text(markup: string): string {
  const container = document.createElement("div");
  container.innerHTML = markup;
  return container.textContent ?? "";
}

// The toggle is a Radix switch, so reach it through the label association
// SettingField sets up rather than by scanning the markup for "disabled".
function toggleDisabled(markup: string, label: string): boolean {
  const container = document.createElement("div");
  container.innerHTML = markup;
  const labelEl = Array.from(container.querySelectorAll("label")).find(
    (el) => el.textContent?.trim() === label,
  );
  if (!labelEl?.htmlFor) throw new Error(`no label found for ${label}`);
  const control = container.querySelector(`[id="${labelEl.htmlFor}"]`);
  if (!control) throw new Error(`no control found for ${label}`);
  return control.hasAttribute("disabled");
}

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
    storageAvailableMock.mockReturnValue(true);
    markerCapabilitiesMock.mockReturnValue({ data: { detection_kind_settings: true } });
  });

  it("offers the server-wide real-time monitoring switch, on by default", () => {
    const rendered = render({ "catalog.search.provider": "postgres" });

    expect(text(rendered)).toContain("Real-time monitoring");
    expect(text(rendered)).toContain(
      "Scan automatically when files in library folders change. Silo scans only what changed, usually within seconds. Works on local disks; network shares (NFS, SMB) aren't supported. Libraries can opt out individually.",
    );
    expect(toggleDisabled(rendered, "Real-time monitoring")).toBe(false);
    expect(toggleChecked(rendered, "Real-time monitoring")).toBe(true);

    const calls = useSettingsFormMock.mock.calls;
    const keys: string[] = calls[calls.length - 1]?.[0]?.keys ?? [];
    expect(keys).toEqual(
      expect.arrayContaining([
        "metadata.cache_images",
        "catalog.scope_versions_to_library",
        "scanner.workers",
        "matcher.workers",
        "matcher.batch_size",
        "metadata.image_workers",
        "metadata.person_refresh_per_minute",
        "markers.mode",
        "markers.lazy_playback",
        "markers.online_storage",
        "markers.detection_workers",
        "markers.detect_intros",
        "markers.detect_credits",
        "catalog.search.provider",
        "catalog.search.meilisearch.url",
        "catalog.search.meilisearch.api_key",
        "catalog.search.meilisearch.semantic_ratio",
      ]),
    );
    // Hidden tier: still saved through the API, no control on this tab.
    expect(keys).not.toContain("catalog.search.meilisearch.embedder");
    expect(keys).not.toContain("catalog.search.meilisearch.binary_quantized");
    expect(keys).not.toContain("catalog.search.meilisearch.rebuild_batch_size");
    expect(keys).toContain("scanner.realtime_monitoring");
  });

  it("offers the background person lookup rate in the advanced scanning settings", () => {
    // The Advanced section opens on its own when one of its fields is edited.
    const rendered = render({ "catalog.search.provider": "postgres" }, [
      "metadata.person_refresh_per_minute",
    ]);

    expect(text(rendered)).toContain("Person lookups per minute");
    expect(text(rendered)).toContain(
      "How many cast and crew members each server looks up per minute in the background.",
    );
  });

  it("reflects a stored off value for real-time monitoring", () => {
    const rendered = render({ "scanner.realtime_monitoring": "false" });

    expect(toggleChecked(rendered, "Real-time monitoring")).toBe(false);
  });

  it("hides Meilisearch connection fields until that engine is selected", () => {
    expect(text(render({ "catalog.search.provider": "postgres" }))).not.toContain(
      "Meilisearch URL",
    );

    expect(text(render({ "catalog.search.provider": "meilisearch" }))).toContain("Meilisearch URL");
  });

  it("allows caching provider artwork without an S3 bucket", () => {
    storageAvailableMock.mockReturnValue(false);
    const rendered = render({});
    expect(text(rendered)).not.toContain("Artwork storage needs a public S3 bucket");
    expect(toggleDisabled(rendered, "Keep provider artwork")).toBe(false);
  });

  it("offers separate intro and credits detection while this server detects markers", () => {
    for (const mode of ["local", "both"]) {
      const rendered = text(render({ "markers.mode": mode }));
      expect(rendered).toContain("Detection workers");
      expect(rendered).toContain("Detect intros");
      expect(rendered).toContain("Detect credits");
      expect(rendered).toContain("reading the end of each episode and movie");
    }
    for (const mode of ["online", "off"]) {
      const rendered = text(render({ "markers.mode": mode }));
      expect(rendered).not.toContain("Detection workers");
      expect(rendered).not.toContain("Detect intros");
      expect(rendered).not.toContain("Detect credits");
    }
  });

  it("hides the detection switches unless the server honors them", () => {
    const cases: Array<[string, { data?: Record<string, boolean> }]> = [
      ["capabilities not loaded", {}],
      ["a server without detection_kind_settings", { data: { redetect_markers: true } }],
      ["a server with detection_kind_settings off", { data: { detection_kind_settings: false } }],
    ];
    for (const [, capabilities] of cases) {
      markerCapabilitiesMock.mockReturnValue(capabilities);
      const rendered = text(render({ "markers.mode": "local" }));
      expect(rendered).not.toContain("Detect intros");
      expect(rendered).not.toContain("Detect credits");
      expect(rendered).toContain("Detection workers");
    }
  });

  it("shows each detection switch's saved state and defaults both on", () => {
    const defaults = render({ "markers.mode": "both" });
    expect(toggleChecked(defaults, "Detect intros")).toBe(true);
    expect(toggleChecked(defaults, "Detect credits")).toBe(true);

    const introsOnly = render({ "markers.mode": "local", "markers.detect_credits": "false" });
    expect(toggleChecked(introsOnly, "Detect intros")).toBe(true);
    expect(toggleChecked(introsOnly, "Detect credits")).toBe(false);

    const creditsOnly = render({ "markers.mode": "local", "markers.detect_intros": "false" });
    expect(toggleChecked(creditsOnly, "Detect intros")).toBe(false);
    expect(toggleChecked(creditsOnly, "Detect credits")).toBe(true);
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
