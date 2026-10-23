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

const makeForm = (values: Record<string, string>, dirty: string[] = []) =>
  fakeSettingsForm(values, {
    dirty,
    buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
  });

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
});
