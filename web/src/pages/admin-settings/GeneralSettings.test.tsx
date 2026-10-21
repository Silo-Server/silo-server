import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import GeneralSettings from "./GeneralSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

const settingsFormMock = vi.fn();
const useCheckAdminSettingsConnectionMock = vi.fn();

// Most cases drive the page from a hand-written form. The Redis editing cases
// that have to prove a value reaches the server flip this and run the real hook.
const { realForm, serverSettings, sensitiveStatus, updateSettingsMock } = vi.hoisted(() => ({
  realForm: { enabled: false },
  serverSettings: { current: {} as Record<string, string> },
  sensitiveStatus: { current: { configured: [] as string[], managed_by_env: [] as string[] } },
  updateSettingsMock: vi.fn(),
}));

vi.mock("@/hooks/useSettingsForm", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/useSettingsForm")>();
  return {
    useSettingsForm: (options: { keys: string[] }) =>
      realForm.enabled ? actual.useSettingsForm(options) : settingsFormMock(options),
  };
});

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: (...args: unknown[]) =>
    useCheckAdminSettingsConnectionMock(...args),
  useAdminServerSettings: () => ({ data: serverSettings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: sensitiveStatus.current, isError: false }),
  useUpdateServerSettings: () => ({ mutateAsync: updateSettingsMock, isPending: false }),
}));

const RESTART_KEYS = new Set<string>([
  "server.lan_discovery",
  "clientip.trusted_proxies",
  "database.max_connections",
  "redis.url",
  "redis.db",
]);
const restartKeysMock = vi.fn(() => ({ has: (key: string) => RESTART_KEYS.has(key) }));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => restartKeysMock(),
}));

const makeForm = (values: Record<string, string> = {}, overrides: Record<string, unknown> = {}) =>
  fakeSettingsForm(values, overrides);

const network = () => within(screen.getByRole("group", { name: "Network" }));
const redis = () => within(screen.getByRole("group", { name: "Redis" }));

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
    settingsFormMock.mockReset();
    settingsFormMock.mockReturnValue(makeForm({ "branding.server_name": "Silo" }));
    useCheckAdminSettingsConnectionMock.mockReset();
    useCheckAdminSettingsConnectionMock.mockReturnValue({
      isPending: false,
      mutateAsync: vi.fn(),
    });
  });

  it("locks trusted proxies when SILO_TRUSTED_PROXIES manages them", () => {
    settingsFormMock.mockReturnValue(
      makeForm({}, { sensitiveManagedByEnv: ["clientip.trusted_proxies"] }),
    );
    renderPage();

    expect(network().getByLabelText("Trusted proxies")).toBeDisabled();
    expect(network().getByText("Managed by SILO_TRUSTED_PROXIES.")).toBeInTheDocument();
  });

  it("keeps the Redis connection check available when REDIS_URL comes from the environment", () => {
    settingsFormMock.mockReturnValue(
      makeForm({}, { sensitiveConfigured: ["redis.url"], sensitiveManagedByEnv: ["redis.url"] }),
    );
    renderPage();

    // The value stays read-only — only writes are refused for env-managed keys —
    // but the check runs against the value the server merged from REDIS_URL.
    expect(redis().getByLabelText("Connection URL")).toBeDisabled();
    expect(redis().getByRole("button", { name: "Check Connection" })).toBeEnabled();
    // The switch already stages the empty URL, so the field offers no clear.
    expect(redis().queryByRole("button", { name: "Clear saved value" })).toBeNull();
  });

  it("holds the Redis switch and database number while a save is in flight", () => {
    settingsFormMock.mockReturnValue(
      makeForm({ "redis.db": "3" }, { sensitiveConfigured: ["redis.url"], isSaving: true }),
    );
    renderPage();

    expect(redis().getByRole("switch", { name: "Use Redis" })).toBeDisabled();
    expect(redis().getByLabelText("Database number")).toBeDisabled();
  });

  it("makes the Redis database number read-only when REDIS_URL comes from the environment", () => {
    settingsFormMock.mockReturnValue(
      makeForm(
        { "redis.db": "1" },
        {
          sensitiveConfigured: ["redis.url"],
          sensitiveManagedByEnv: ["redis.url", "redis.db"],
        },
      ),
    );
    renderPage();

    // The server reports the number in REDIS_URL; it cannot be saved here.
    expect(redis().getByLabelText("Database number")).toHaveValue(1);
    expect(redis().getByLabelText("Database number")).toBeDisabled();
  });

  it("holds only Redis when protected credential status cannot be loaded", () => {
    settingsFormMock.mockReturnValue(
      makeForm({}, { sensitiveStatusReady: false, sensitiveStatusError: true }),
    );
    renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent("Redis settings are unavailable");
    expect(screen.queryByRole("group", { name: "Redis" })).toBeNull();
    expect(network().getByLabelText("Silo public URL")).toBeEnabled();
  });

  it("saves other edits but not a Redis draft hidden by a credential status error", async () => {
    const form = makeForm(
      {},
      {
        sensitiveStatusReady: false,
        sensitiveStatusError: true,
        dirtyCount: 2,
        dirtyKeys: ["server.public_url", "redis.url"],
        isDirty: (key: string) => key === "server.public_url" || key === "redis.url",
      },
    );
    settingsFormMock.mockReturnValue(form);
    renderPage();

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(form.save).toHaveBeenCalledWith(["server.public_url"]);
  });

  describe("editing the Redis connection", () => {
    const newUrl = "redis://new.example.invalid:6379/7";

    beforeEach(() => {
      realForm.enabled = true;
      // No redis.db is saved: the server reports the 3 in the saved URL.
      serverSettings.current = { "redis.db": "3" };
      sensitiveStatus.current = { configured: ["redis.url"], managed_by_env: [] };
      updateSettingsMock.mockReset();
      updateSettingsMock.mockResolvedValue({ values: {}, restart_required: false });
    });

    afterEach(() => {
      realForm.enabled = false;
    });

    it("saves the number on screen with a new URL", async () => {
      renderPage();

      await userEvent.type(redis().getByLabelText("Connection URL"), newUrl);

      expect(redis().getByLabelText("Database number")).toHaveValue(3);
      await userEvent.click(screen.getByRole("button", { name: "Save" }));
      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({ "redis.url": newUrl, "redis.db": "3" }),
      );
    });

    it("checks a new URL with the number on screen", async () => {
      const mutateAsync = vi.fn().mockResolvedValue({ success: true, message: "ok" });
      useCheckAdminSettingsConnectionMock.mockReturnValue({ isPending: false, mutateAsync });
      renderPage();

      await userEvent.type(redis().getByLabelText("Connection URL"), newUrl);
      await userEvent.click(redis().getByRole("button", { name: "Check Connection" }));

      await waitFor(() =>
        expect(mutateAsync).toHaveBeenCalledWith({
          kind: "redis",
          body: {
            values: { "redis.url": newUrl, "redis.db": "3" },
            dirty_keys: ["redis.url", "redis.db"],
          },
        }),
      );
    });

    it("stages nothing once the new URL is taken back", async () => {
      renderPage();

      await userEvent.type(redis().getByLabelText("Connection URL"), newUrl);
      expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
      await userEvent.clear(redis().getByLabelText("Connection URL"));

      expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument();
      expect(redis().getByLabelText("Database number")).toHaveValue(3);
    });

    it("drops a URL keystroke that arrives while a save is in flight", async () => {
      updateSettingsMock.mockReturnValue(new Promise(() => {}));
      renderPage();
      fireEvent.change(redis().getByLabelText("Database number"), { target: { value: "5" } });
      await userEvent.click(screen.getByRole("button", { name: "Save" }));
      await waitFor(() => expect(updateSettingsMock).toHaveBeenCalledWith({ "redis.db": "5" }));

      fireEvent.change(redis().getByLabelText("Connection URL"), { target: { value: "r" } });

      expect(redis().getByLabelText("Connection URL")).toHaveValue("");
    });

    it("sends the number with the empty URL when Redis is switched off", async () => {
      // The server stores no number without a URL, so this removes a saved one.
      renderPage();

      await userEvent.click(screen.getByRole("switch", { name: "Use Redis" }));
      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({ "redis.url": "", "redis.db": "3" }),
      );
    });

    it("stages nothing once Redis is switched back on", async () => {
      renderPage();

      await userEvent.click(screen.getByRole("switch", { name: "Use Redis" }));
      expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("switch", { name: "Use Redis" }));

      expect(screen.queryByText(/unsaved change/)).not.toBeInTheDocument();
      expect(redis().getByLabelText("Database number")).toHaveValue(3);
    });
  });
});
