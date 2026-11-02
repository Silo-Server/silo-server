import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import SearchSettings from "./SearchSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

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

const makeForm = (
  values: Record<string, string>,
  dirty: string[] = [],
  overrides: Record<string, unknown> = {},
) =>
  fakeSettingsForm(values, {
    dirty,
    buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
    ...overrides,
  });

/** The page's one group, from static markup. */
function render(
  values: Record<string, string>,
  dirty: string[] = [],
  overrides: Record<string, unknown> = {},
): HTMLElement {
  useSettingsFormMock.mockReturnValue(makeForm(values, dirty, overrides));
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

describe("SearchSettings", () => {
  beforeEach(() => {
    localStorage.clear();
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

  it("keeps an unsaved Meilisearch change in view after switching back to Postgres", () => {
    const group = render({ "catalog.search.provider": "postgres" }, [
      "catalog.search.provider",
      "catalog.search.meilisearch.url",
    ]);

    expect(group.textContent).toContain("Meilisearch URL");
  });

  it("locks the connection fields the environment sets, and only those", () => {
    const group = render(
      {
        "catalog.search.provider": "meilisearch",
        "catalog.search.meilisearch.url": "http://meilisearch:7700",
      },
      [],
      {
        sensitiveManagedByEnv: ["catalog.search.provider", "catalog.search.meilisearch.url"],
      },
    );

    expect(group.querySelector("button[role=combobox]")?.hasAttribute("disabled")).toBe(true);
    expect(group.textContent).toContain("Managed by SILO_SEARCH_PROVIDER.");
    expect(
      group.querySelector("input[value='http://meilisearch:7700']")?.hasAttribute("disabled"),
    ).toBe(true);
    expect(group.querySelector("input[type=password]")?.hasAttribute("disabled")).toBe(false);
  });

  it("holds the connection fields when it cannot tell which ones the environment sets", () => {
    const group = render({ "catalog.search.provider": "meilisearch" }, [], {
      sensitiveStatusError: true,
    });

    expect(group.querySelector("button[role=combobox]")?.hasAttribute("disabled")).toBe(true);
    expect(group.querySelector("input[type=password]")?.hasAttribute("disabled")).toBe(true);
    expect(group.textContent).toContain("Protected credential status could not be loaded.");
  });
});
