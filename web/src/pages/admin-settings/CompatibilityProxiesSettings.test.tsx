import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

describe("CompatibilityProxiesSettings", () => {
  it("shows Jellyfin and Audiobookshelf proxy settings", () => {
    useSettingsFormMock.mockReturnValue({
      isLoading: false,
      getValue: (key: string) => {
        if (key === "audiobookshelf_compat.enabled") return "true";
        if (key === "jellyfin_compat.public_url") return "https://jellyfin.example.test";
        return "";
      },
      setValue: vi.fn(),
      dirtyCount: 0,
      save: vi.fn(),
      discard: vi.fn(),
      isSaving: false,
      restartRequired: false,
      sensitiveConfigured: [],
      sensitiveManagedByEnv: [],
      buildConnectionCheckRequest: vi.fn(),
    });

    const markup = renderToStaticMarkup(<CompatibilityProxiesSettings />);

    expect(useSettingsFormMock).toHaveBeenCalledWith({
      keys: expect.arrayContaining([
        "jellyfin_compat.public_url",
        "jellyfin_compat.server_name",
        "jellyfin_compat.web_enabled",
        "audiobookshelf_compat.enabled",
      ]),
    });
    expect(markup).toContain("Compatibility Proxies");
    expect(markup).toContain("Jellyfin");
    expect(markup).toContain("Audiobookshelf");
    expect(markup).toContain("Enable Audiobookshelf Proxy");
    expect(markup).not.toContain("Listen Address");
    expect(markup).not.toContain("https://abs.example.test");
  });
});
