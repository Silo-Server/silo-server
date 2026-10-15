import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import SearchSettings from "./SearchSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useCatalogSearchStatus: () => ({ data: undefined, isLoading: true }),
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

/** The page's one group, from static markup. */
function render(values: Record<string, string>, dirty: string[] = []): HTMLElement {
  useSettingsFormMock.mockReturnValue(makeForm(values, dirty));
  const container = document.createElement("div");
  container.innerHTML = renderToStaticMarkup(
    <MemoryRouter>
      <SearchSettings />
    </MemoryRouter>,
  );
  const group = container.querySelector<HTMLElement>("section[role=group]");
  if (!group) throw new Error("no group rendered");
  return group;
}

/** Opens the Advanced disclosure via its persisted state. */
function expand() {
  localStorage.setItem("silo.admin.advanced.search", "true");
}

/** The accessible names of a group's disclosure buttons. */
function disclosures(group: HTMLElement): string[] {
  return Array.from(group.querySelectorAll("button[aria-expanded]"), (button) =>
    button.getAttribute("aria-label"),
  ).filter((label): label is string => label !== null);
}

describe("SearchSettings", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("loads the search keys and leaves the hidden tier out", () => {
    render({});

    const calls = useSettingsFormMock.mock.calls;
    const keys: string[] = calls[calls.length - 1]?.[0]?.keys ?? [];
    expect(keys).toEqual(
      expect.arrayContaining([
        "catalog.search.provider",
        "catalog.search.meilisearch.url",
        "catalog.search.meilisearch.api_key",
        "catalog.search.meilisearch.matching_strategy",
        "catalog.search.meilisearch.semantic_enabled",
        "catalog.search.meilisearch.semantic_ratio",
      ]),
    );
    // Hidden tier: still saved through the API, no control on this page.
    expect(keys).not.toContain("catalog.search.meilisearch.embedder");
    expect(keys).not.toContain("catalog.search.meilisearch.binary_quantized");
    expect(keys).not.toContain("catalog.search.meilisearch.rebuild_batch_size");
  });

  it("hides Meilisearch connection fields until that engine is selected", () => {
    expand();
    expect(render({ "catalog.search.provider": "postgres" }).textContent).not.toContain(
      "Meilisearch URL",
    );

    expect(render({ "catalog.search.provider": "meilisearch" }).textContent).toContain(
      "Meilisearch URL",
    );
  });

  it("shows only the engine and a status disclosure with Postgres", () => {
    const group = render({ "catalog.search.provider": "postgres" });
    expect(group.querySelector("h3")?.textContent).toBe("Search");
    expect(group.textContent).toContain(
      "Built-in Postgres search needs no setup. Meilisearch is optional.",
    );
    expect(group.textContent).toContain("Search engine");
    // Only the status panel is behind it, and that is not a setting.
    expect(disclosures(group)).toEqual(["Advanced"]);
  });

  it("shows the Meilisearch connection and result choices, keeping tuning behind Advanced", () => {
    const collapsed = render({ "catalog.search.provider": "meilisearch" });
    for (const label of [
      "Meilisearch URL",
      "Meilisearch API key",
      "Check Connection",
      "When a search has several words",
      "Match by meaning as well as words",
    ]) {
      expect(collapsed.textContent).toContain(label);
    }
    // Index prefix, timeout, batch size and the meaning-based share.
    expect(disclosures(collapsed)).toEqual(["Advanced · 4 settings"]);
    for (const label of [
      "Index name prefix",
      "Query timeout",
      "Items sent to the index per batch",
      "Meaning-based share of results",
    ]) {
      expect(collapsed.textContent).not.toContain(label);
    }
    expect(collapsed.querySelector("h4")).toBeNull();

    expand();
    const open = render({ "catalog.search.provider": "meilisearch" });
    expect(open.textContent).toContain("Index name prefix");
    expect(open.textContent).toContain("Meaning-based share of results");
    expect(open.querySelector("h4")?.textContent).toBe("Search status");
  });

  it("keeps an unsaved Meilisearch change in view after switching back to Postgres", () => {
    const group = render({ "catalog.search.provider": "postgres" }, [
      "catalog.search.provider",
      "catalog.search.meilisearch.url",
    ]);

    expect(group.textContent).toContain("Meilisearch URL");
  });

  it("opens the disclosure for an unsaved tuning change", () => {
    const group = render({ "catalog.search.provider": "meilisearch" }, [
      "catalog.search.meilisearch.timeout_ms",
    ]);

    expect(group.textContent).toContain("Query timeout");
  });
});
