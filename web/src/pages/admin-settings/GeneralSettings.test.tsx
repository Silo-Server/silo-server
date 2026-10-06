import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import GeneralSettings from "./GeneralSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(["server.log_level"]),
}));

function makeForm(values: Record<string, string> = {}) {
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    isDirty: () => false,
    dirtyCount: 0,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
  };
}

function renderPage() {
  return render(
    <MemoryRouter>
      <GeneralSettings />
    </MemoryRouter>,
  );
}

describe("GeneralSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    useSettingsFormMock.mockReset();
    useSettingsFormMock.mockReturnValue(makeForm({ "signup.enabled": "true" }));
  });

  it("manages identity, signup and logging keys on one save bar", () => {
    renderPage();

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toEqual([
      "server.public_url",
      "server.lan_discovery",
      "branding.server_name",
      "branding.login_subtitle",
      "signup.enabled",
      "password_reset.self_service_enabled",
      "server.log_level",
      "server.log_quiet",
    ]);
  });
});
