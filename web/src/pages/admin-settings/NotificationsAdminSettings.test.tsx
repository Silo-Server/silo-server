import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import NotificationsAdminSettings from "./NotificationsAdminSettings";

const useSettingsFormMock = vi.fn();
const restartKeysMock = vi.fn(() => new Set<string>());
const updateSettingsMock = vi.fn(() => Promise.resolve({ values: {}, restart_required: false }));

const mocks = vi.hoisted(() => ({
  copyTextToClipboard: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/lib/clipboard", () => ({
  copyTextToClipboard: (...args: unknown[]) => mocks.copyTextToClipboard(...args),
}));

vi.mock("sonner", () => ({
  toast: { error: mocks.toastError, success: mocks.toastSuccess },
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useUpdateServerSettings: () => ({ mutateAsync: updateSettingsMock, isPending: false }),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => restartKeysMock(),
}));

vi.mock("@/hooks/queries/admin/serverNotificationChannels", () => ({
  useServerNotificationChannels: () => ({ data: [] }),
}));

function makeForm(overrides: Record<string, string> = {}) {
  return {
    isLoading: false,
    loadError: false,
    loaded: true,
    getValue: (key: string) => {
      if (key in overrides) return overrides[key];
      switch (key) {
        case "notifications.release_events_enabled":
        case "notifications.fanout_enabled":
        case "notifications.ui_enabled":
        case "notifications.web_push_enabled":
        case "notifications.apple_push_delivery_enabled":
        case "notifications.android_push_delivery_enabled":
          return "true";
        case "notifications.push_relay_url":
          return "https://push.siloserver.org";
        case "notifications.push_relay_deployment_id":
          return "01DEPLOYMENT";
        case "notifications.push_relay_key_prefix":
          return "cap_v1_test";
        case "notifications.push_relay_expires_at":
          // Relative so the "renews automatically" (not-yet-expired) branch
          // stays stable — a hardcoded date turned into a time bomb once the
          // calendar passed it.
          return new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
        default:
          return "";
      }
    },
    setValue: vi.fn(),
    resetValue: vi.fn(),
    dirtyCount: 0,
    dirtyKeys: [],
    isDirty: vi.fn((_key: string) => false),
    isClearStaged: vi.fn((_key: string) => false),
    save: vi.fn(() => Promise.resolve()),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: ["notifications.push_relay_api_key"],
    sensitiveManagedByEnv: [],
    buildConnectionCheckRequest: vi.fn(),
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  return (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/admin/settings/notifications"]}>
        <NotificationsAdminSettings />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

/** Opens one channel card by the description in its header button. */
async function openChannel(pattern: RegExp) {
  await userEvent.click(screen.getByRole("button", { name: pattern }));
}

const EMAIL_CHANNEL = /Daily summary or a message per episode/;
const DISCORD_CHANNEL = /Direct messages from your Discord bot/;

describe("NotificationsAdminSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    restartKeysMock.mockReturnValue(new Set<string>());
    updateSettingsMock.mockClear();
    mocks.copyTextToClipboard.mockReset();
    mocks.copyTextToClipboard.mockResolvedValue(undefined);
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("shows the Silo Push Relay channel status", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());

    const keys = useSettingsFormMock.mock.calls.at(-1)?.[0]?.keys as string[];
    expect(keys).toEqual(
      expect.arrayContaining([
        "notifications.apple_push_delivery_enabled",
        "notifications.android_push_delivery_enabled",
        "notifications.push_relay_deployment_id",
        "notifications.push_relay_expires_at",
        "notifications.push_relay_key_prefix",
        "notifications.push_relay_reregistration_required",
      ]),
    );
    expect(keys).not.toContain("notifications.push_relay_api_key");
    expect(keys).toContain("notifications.push_relay_url");
    for (const key of [
      "email.enabled",
      "email.smtp_host",
      "email.smtp_port",
      "email.smtp_security",
      "email.smtp_username",
      "email.smtp_password",
      "email.from_address",
      "email.from_name",
    ]) {
      expect(keys).toContain(key);
    }
    for (const key of ["discord.client_id", "discord.client_secret", "discord.bot_token"]) {
      expect(keys).toContain(key);
    }

    expect(screen.getByText("Silo Push Relay")).toBeInTheDocument();
    expect(screen.getByText(/delivered by APNs or FCM/)).toBeInTheDocument();
    expect(screen.getByText("Relay configured")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Silo Push Relay/ }));

    expect(screen.getByText("Privacy disclosure")).toBeInTheDocument();
    expect(screen.getByText("Apple Push (APNs)")).toBeInTheDocument();
    expect(screen.getByText("Android Push (FCM)")).toBeInTheDocument();
    expect(screen.getByText(/content-free request to Silo's push relay/)).toBeInTheDocument();
    expect(screen.getByText(/does not receive notification titles/)).toBeInTheDocument();
    expect(screen.getByText(/fetches private content directly/)).toBeInTheDocument();
    expect(screen.getByText("Deployment ID")).toBeInTheDocument();
    expect(screen.getByText("Rotate credential")).toBeInTheDocument();
    expect(screen.getByText("Credential: cap_v1_test")).toBeInTheDocument();
    expect(screen.getByText(/Silo renews automatically/)).toBeInTheDocument();
    expect(screen.queryByText("Relay API Key")).not.toBeInTheDocument();
    expect(screen.queryByText("Smoke Test Profile ID")).not.toBeInTheDocument();
    expect(screen.queryByText("Server Device ID")).not.toBeInTheDocument();
    expect(screen.queryByText("Send test push")).not.toBeInTheDocument();
  });

  it("shows the saved SMTP password as a masked, editable input", async () => {
    const form = makeForm();
    form.sensitiveConfigured = ["email.smtp_password"];
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());
    await openChannel(EMAIL_CHANNEL);

    // No Replace step: typing stages a replacement, blank keeps the saved one.
    const input = screen.getByLabelText("Password");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    expect(screen.queryByRole("button", { name: /Replace/ })).not.toBeInTheDocument();
  });

  it("confirms the invite link copy only once it has actually happened", async () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "discord.client_id": "1234567890" }));

    render(renderPage());
    await openChannel(DISCORD_CHANNEL);
    await userEvent.click(screen.getByRole("button", { name: /Copy link/ }));

    expect(mocks.copyTextToClipboard).toHaveBeenCalledWith(
      expect.stringContaining("client_id=1234567890"),
    );
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith("Invite link copied"));
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("says so when the invite link could not be copied", async () => {
    // Denied permission, or a browser that only exposes the clipboard on a
    // secure origin — which a LAN server reached over plain HTTP is not.
    mocks.copyTextToClipboard.mockRejectedValue(new Error("clipboard blocked"));
    useSettingsFormMock.mockReturnValue(makeForm({ "discord.client_id": "1234567890" }));

    render(renderPage());
    await openChannel(DISCORD_CHANNEL);
    await userEvent.click(screen.getByRole("button", { name: /Copy link/ }));

    await waitFor(() =>
      expect(mocks.toastError).toHaveBeenCalledWith(
        "Couldn't copy the invite link — select it and copy it manually",
      ),
    );
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });

  it("saves the Discord application on its own, not through the page save bar", async () => {
    const form = makeForm({ "discord.client_id": "1234567890" });
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());
    await openChannel(DISCORD_CHANNEL);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(updateSettingsMock).toHaveBeenCalledWith({ "discord.client_id": "1234567890" });
    expect(form.save).not.toHaveBeenCalled();
  });

  it("clears the Discord application behind a confirmation", async () => {
    const form = makeForm({ "discord.client_id": "1234567890" });
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());
    await openChannel(DISCORD_CHANNEL);
    await userEvent.click(screen.getByRole("button", { name: "Clear credentials" }));

    expect(screen.getByText("Clear Discord app credentials?")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /^Clear$/ }));

    expect(updateSettingsMock).toHaveBeenCalledWith({
      "discord.client_id": "",
      "discord.client_secret": "",
      "discord.bot_token": "",
    });
  });
});
