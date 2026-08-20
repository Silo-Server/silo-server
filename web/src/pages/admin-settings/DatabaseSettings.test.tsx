import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import DatabaseSettings from "./DatabaseSettings";
import { USER_DATABASE_BACKEND_OPTIONS } from "./databaseSettingOptions";

const useSettingsFormMock = vi.fn();
const useCheckAdminSettingsConnectionMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: (...args: unknown[]) =>
    useCheckAdminSettingsConnectionMock(...args),
}));

function makeForm(redisUrl: string, managedByEnv = false) {
  return {
    isLoading: false,
    getValue: (key: string) => {
      if (key === "redis.url") return redisUrl;
      return "";
    },
    setValue: vi.fn(),
    dirtyCount: 0,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: redisUrl ? ["redis.url"] : [],
    sensitiveManagedByEnv: managedByEnv ? ["redis.url"] : [],
    buildConnectionCheckRequest: vi.fn(),
  };
}

describe("DatabaseSettings", () => {
  useCheckAdminSettingsConnectionMock.mockReturnValue({
    isPending: false,
    mutateAsync: vi.fn(),
  });

  it("shows Redis controls in the Database tab", () => {
    useSettingsFormMock.mockReturnValue(makeForm(""));

    const markup = renderToStaticMarkup(<DatabaseSettings />);

    expect(markup).toContain("Redis");
    expect(markup).toContain("Enable Redis");
    expect(markup).not.toContain("Connection URL");
  });

  it("marks the unimplemented SQLite backend as unavailable", () => {
    expect(USER_DATABASE_BACKEND_OPTIONS).toContainEqual({
      value: "sqlite",
      label: "SQLite (TBD)",
      disabled: true,
    });
  });

  it("shows the Redis connection URL when Redis is enabled", () => {
    useSettingsFormMock.mockReturnValue(makeForm("redis://cache:6379"));

    const markup = renderToStaticMarkup(<DatabaseSettings />);

    expect(markup).toContain("Enable Redis");
    expect(markup).toContain("Connection URL");
    expect(markup).toContain("Check Connection");
  });

  it("shows when Redis is managed by environment configuration", () => {
    useSettingsFormMock.mockReturnValue(makeForm("", true));

    const markup = renderToStaticMarkup(<DatabaseSettings />);

    expect(markup).toContain("Managed by environment");
    expect(markup).toContain("REDIS_URL");
    expect(markup).toContain("This setting is controlled by REDIS_URL");
  });
});
