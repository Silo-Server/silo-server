import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(["jellyfin_compat.session_ttl"]),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useJellyfinCompatStatus: () => ({
    isLoading: false,
    data: {
      enabled: false,
      web_enabled: false,
      api_state: "stopped",
      web_state: "missing",
      prerequisites: [],
    },
  }),
  useInstallJellyfinCompatWeb: () => ({ mutate: vi.fn(), isPending: false }),
  useRemoveJellyfinCompatWeb: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateJellyfinCompatSettings: () => ({ mutate: vi.fn(), isPending: false }),
}));

function mockForm(overrides: Record<string, unknown> = {}) {
  useSettingsFormMock.mockReturnValue({
    isLoading: false,
    getValue: (key: string) => {
      if (key === "audiobookshelf_compat.enabled") return "true";
      if (key === "jellyfin_compat.public_url") return "https://jellyfin.example.test";
      return "";
    },
    setValue: vi.fn(),
    dirtyCount: 0,
    dirtyKeys: [],
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
    buildConnectionCheckRequest: vi.fn(),
    ...overrides,
  });
}

describe("CompatibilityProxiesSettings", () => {
  it("renders the page header and every field group heading", () => {
    mockForm();

    const markup = renderToStaticMarkup(<CompatibilityProxiesSettings />);

    for (const heading of ["Jellyfin", "Audiobookshelf"]) {
      expect(markup).toContain(heading);
    }
    expect(markup).toContain("Compatibility");
  });

  it("states the Jellyfin web player install state in plain wording", () => {
    mockForm();

    const markup = renderToStaticMarkup(<CompatibilityProxiesSettings />);

    expect(markup).toContain("Jellyfin web player");
    expect(markup).toContain("Not installed");
  });

  it("shows the essential proxy controls and keeps identity settings behind Advanced", () => {
    mockForm();

    const markup = renderToStaticMarkup(<CompatibilityProxiesSettings />);

    expect(useSettingsFormMock).toHaveBeenCalledWith({
      keys: expect.arrayContaining([
        "jellyfin_compat.public_url",
        "jellyfin_compat.server_name",
        "jellyfin_compat.web_enabled",
        "audiobookshelf_compat.enabled",
      ]),
    });
    expect(markup).toContain("Allow Jellyfin apps to connect");
    expect(markup).toContain("Address Jellyfin apps should use");
    expect(markup).toContain("Allow Audiobookshelf apps to connect");
    expect(markup).toContain("Advanced · 7 settings");
    // Collapsed by default, so the advanced fields are not rendered at all.
    expect(markup).not.toContain("Server ID");
    expect(markup).not.toContain("Web player install folder");
    // Internal enum names never reach the admin.
    expect(markup).not.toContain("web_state");
    expect(markup).not.toContain("Provenance present");
    expect(markup).not.toContain("Listen Address");
  });

  it("opens Advanced while one of its fields is unsaved", () => {
    mockForm({ dirtyKeys: ["jellyfin_compat.web_version"], dirtyCount: 1 });

    const markup = renderToStaticMarkup(<CompatibilityProxiesSettings />);

    expect(markup).toContain("Web player version to install");
    expect(markup).toContain("Save your changes before installing or removing the web player.");
  });
});
