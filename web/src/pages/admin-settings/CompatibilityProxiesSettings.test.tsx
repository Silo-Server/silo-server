import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";

const useSettingsFormMock = vi.fn();
const useJellyfinCompatStatusMock = vi.fn();
const useInstallJellyfinCompatWebMock = vi.fn();
const useRemoveJellyfinCompatWebMock = vi.fn();
const useUpdateJellyfinCompatSettingsMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useJellyfinCompatStatus: (...args: unknown[]) => useJellyfinCompatStatusMock(...args),
  useInstallJellyfinCompatWeb: (...args: unknown[]) => useInstallJellyfinCompatWebMock(...args),
  useRemoveJellyfinCompatWeb: (...args: unknown[]) => useRemoveJellyfinCompatWebMock(...args),
  useUpdateJellyfinCompatSettings: (...args: unknown[]) =>
    useUpdateJellyfinCompatSettingsMock(...args),
}));

describe("CompatibilityProxiesSettings", () => {
  it("shows Jellyfin and Audiobookshelf proxy settings", () => {
    useJellyfinCompatStatusMock.mockReturnValue({
      data: {
        enabled: true,
        api_state: "enabled",
        listen: "",
        public_url: "",
        emulated_server_version: "10.10.7",
        server_name: "Silo",
        web_enabled: false,
        web_state: "missing",
        pinned_version: "",
        source_url: "",
        install_root: "",
        install_path: "",
        license_present: false,
        provenance_present: false,
        installer_ready: true,
        prerequisites: [],
        restart_required: false,
      },
      isLoading: false,
    });
    useInstallJellyfinCompatWebMock.mockReturnValue({ isPending: false, mutate: vi.fn() });
    useRemoveJellyfinCompatWebMock.mockReturnValue({ isPending: false, mutate: vi.fn() });
    useUpdateJellyfinCompatSettingsMock.mockReturnValue({ isPending: false, mutate: vi.fn() });
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
