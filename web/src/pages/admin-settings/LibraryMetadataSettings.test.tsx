import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import LibraryMetadataSettings from "./LibraryMetadataSettings";

const useSettingsFormMock = vi.fn();
const useRestartKeysMock = vi.fn(() => new Set<string>());
const storageAvailableMock = vi.fn(() => true);

vi.mock("@/hooks/useBranding", () => ({
  useBranding: () => ({ storageAvailable: storageAvailableMock() }),
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

describe("LibraryMetadataSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    useRestartKeysMock.mockReturnValue(new Set<string>());
    storageAvailableMock.mockReturnValue(true);
  });

  it("renders every field group heading", () => {
    const rendered = text(render({ "catalog.search.provider": "meilisearch" }));

    for (const heading of ["Artwork", "Browsing", "Scanning", "Skip markers", "Search"]) {
      expect(rendered).toContain(heading);
    }
  });

  it("renders the tab title and the essential controls", () => {
    const rendered = text(render({ "catalog.search.provider": "postgres" }));

    expect(rendered).toContain("Library & Metadata");
    expect(rendered).toContain("Keep provider artwork");
    expect(rendered).toContain("Marker source");
    expect(rendered).toContain("Search engine");
  });

  it("states the CPU cost of local detection and the search fallback guarantee", () => {
    const rendered = text(render({ "catalog.search.provider": "meilisearch" }));

    expect(rendered).toContain("Online markers take priority.");
    expect(rendered).toContain("Local detection uses CPU.");
    expect(rendered).toContain(
      "Meilisearch tolerates typos but runs as its own service. If it goes down, search falls back to the built-in engine automatically.",
    );
  });

  it("offers the Meilisearch connection check as a filled button", () => {
    const container = document.createElement("div");
    container.innerHTML = render({ "catalog.search.provider": "meilisearch" });

    const button = Array.from(container.querySelectorAll("button")).find(
      (el) => el.textContent?.trim() === "Check Connection",
    );

    expect(button).toBeDefined();
    // Filled rather than the transparent outline variant, so the control does
    // not read as flat text inside the group panel.
    expect(button?.getAttribute("data-variant")).toBe("secondary");
  });

  it.each(["stored", "on_demand"])("explains scheduled sync for %s storage", (storage) => {
    const rendered = text(render({ "markers.mode": "online", "markers.online_storage": storage }));

    if (storage === "stored") {
      expect(rendered).toContain("daily at 03:00 (server time) by default");
      expect(rendered).not.toContain("Scheduled online sync is disabled.");
    } else {
      expect(rendered).toContain("Scheduled online sync is disabled.");
      expect(rendered).not.toContain("daily at 03:00");
    }
  });

  it("manages the merged key set of the three tabs it replaces", () => {
    render({});

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
        "markers.mode",
        "markers.lazy_playback",
        "markers.online_storage",
        "markers.detection_workers",
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
  });

  it("keeps marker behavior and points provider setup at the providers page", () => {
    const rendered = render({ "catalog.search.provider": "postgres" });

    expect(text(rendered)).toContain("Marker source");
    // Per-provider configuration moved to Subtitles & Metadata; only the link
    // to it is left here.
    expect(text(rendered)).not.toContain("Get markers from this provider");
    expect(text(rendered)).not.toContain("Minimum confidence");
    expect(text(rendered)).toContain("Marker providers");
    expect(rendered).toContain("/admin/settings/providers");
  });

  it("keeps advanced settings collapsed but expands a section holding a staged edit", () => {
    expect(text(render({ "catalog.search.provider": "postgres" }))).not.toContain(
      "Scanner workers",
    );

    expect(text(render({ "catalog.search.provider": "postgres" }, ["scanner.workers"]))).toContain(
      "Scanner workers",
    );
    expect(
      text(render({ "catalog.search.provider": "postgres" }, ["metadata.image_workers"])),
    ).toContain("Image encoding workers");
  });

  it("offers to restore worker defaults only while a value differs from them", () => {
    const untouched = { "catalog.search.provider": "postgres", "scanner.workers": "8" };
    expect(text(render(untouched))).not.toContain("Restore defaults");

    const form = makeForm({ ...untouched, "metadata.image_workers": "2" });
    useSettingsFormMock.mockReturnValue(form);
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <LibraryMetadataSettings />
      </MemoryRouter>,
    );
    expect(text(markup)).toContain("Restore defaults");
  });

  it("hides Meilisearch connection fields until that engine is selected", () => {
    expect(text(render({ "catalog.search.provider": "postgres" }))).not.toContain(
      "Meilisearch URL",
    );

    expect(text(render({ "catalog.search.provider": "meilisearch" }))).toContain("Meilisearch URL");
  });

  it("leaves artwork storage editable and unannotated while public storage is active", () => {
    const rendered = render({ "s3.public_bucket": "silo-public" });

    expect(text(rendered)).toContain("Keep provider artwork");
    expect(text(rendered)).not.toContain("Restart the server for artwork storage to start");
    expect(text(rendered)).not.toContain("Artwork storage needs a public S3 bucket");
    expect(toggleDisabled(rendered, "Keep provider artwork")).toBe(false);
  });

  it("allows caching provider artwork without an S3 bucket", () => {
    storageAvailableMock.mockReturnValue(false);
    const rendered = render({});
    expect(text(rendered)).not.toContain("Artwork storage needs a public S3 bucket");
    expect(toggleDisabled(rendered, "Keep provider artwork")).toBe(false);
  });

  it("shows detection workers only while this server detects markers", () => {
    expect(text(render({ "markers.mode": "local" }))).toContain("Detection workers");
    expect(text(render({ "markers.mode": "both" }))).toContain("Detection workers");
    expect(text(render({ "markers.mode": "online" }))).not.toContain("Detection workers");
    expect(text(render({ "markers.mode": "off" }))).not.toContain("Detection workers");
  });

  it("says it once for a group where every field needs a restart", () => {
    useRestartKeysMock.mockReturnValue(
      new Set([
        "markers.mode",
        "markers.lazy_playback",
        "markers.online_storage",
        "markers.detection_workers",
      ]),
    );

    const rendered = render({ "catalog.search.provider": "postgres" });

    expect(text(rendered)).toContain("Changes apply after a restart");
    // The group says it, so the field inside drops its own badge.
    expect(rendered).not.toContain("Takes effect after a server restart");
  });

  it("marks a restart-required field with the restart badge inside a mixed group", () => {
    useRestartKeysMock.mockReturnValue(new Set(["scanner.workers"]));

    const rendered = render({ "catalog.search.provider": "postgres" }, ["scanner.workers"]);

    expect(rendered).toContain("Takes effect after a server restart");
    expect(text(rendered)).not.toContain("Changes apply after a restart");
  });

  it("warns that enabling meaning-based search rebuilds the index after restart", () => {
    const values: Record<string, string> = {
      "catalog.search.provider": "meilisearch",
      "catalog.search.meilisearch.semantic_enabled": "true",
    };
    const form = makeForm(values, ["catalog.search.meilisearch.semantic_enabled"]);
    form.getPersistedValue = (key: string) =>
      key === "catalog.search.meilisearch.semantic_enabled" ? "false" : (values[key] ?? "");
    useSettingsFormMock.mockReturnValue(form);

    const rendered = text(
      renderToStaticMarkup(
        <MemoryRouter>
          <LibraryMetadataSettings />
        </MemoryRouter>,
      ),
    );

    expect(rendered).toContain("Enabling this changes the index format");
    expect(rendered).toContain("rebuilds the index automatically");
    expect(rendered).toContain("Keyword search stays available");
  });
});
