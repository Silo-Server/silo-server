import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
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

function renderStaticPage() {
  return renderToStaticMarkup(renderPage());
}

/** Opens one channel card by the description in its header button. */
async function openChannel(pattern: RegExp) {
  await userEvent.click(screen.getByRole("button", { name: pattern }));
}

const EMAIL_CHANNEL = /Daily summary or a message per episode/;
const DISCORD_CHANNEL = /Direct messages from your Discord bot/;
const WEBHOOK_CHANNEL = /Webhooks people create for themselves/;

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

  it("registers Silo Push Relay settings with the shared settings form", () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    renderStaticPage();

    expect(useSettingsFormMock).toHaveBeenCalledWith({
      keys: expect.arrayContaining([
        "notifications.apple_push_delivery_enabled",
        "notifications.android_push_delivery_enabled",
        "notifications.push_relay_deployment_id",
        "notifications.push_relay_expires_at",
        "notifications.push_relay_key_prefix",
        "notifications.push_relay_reregistration_required",
      ]),
    });
    const firstCall = useSettingsFormMock.mock.calls[0];
    if (!firstCall) {
      throw new Error("useSettingsForm was not called");
    }
    const [options] = firstCall as [{ keys: string[] }];
    expect(options.keys).not.toContain("notifications.push_relay_api_key");
    expect(options.keys).toContain("notifications.push_relay_url");
  });

  it("owns the merged email keys and the Discord application", () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    renderStaticPage();

    const calls = useSettingsFormMock.mock.calls;
    const firstCall = calls[calls.length - 1];
    if (!firstCall) {
      throw new Error("useSettingsForm was not called");
    }
    const [options] = firstCall as [{ keys: string[] }];
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
      expect(options.keys).toContain(key);
    }
    for (const key of ["discord.client_id", "discord.client_secret", "discord.bot_token"]) {
      expect(options.keys).toContain(key);
    }
  });

  it("renders every field group heading", () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());

    expect(screen.getByText("Grouping and flood control")).toBeInTheDocument();
    expect(screen.getByText("Retention")).toBeInTheDocument();
    expect(screen.getByText("Pipeline")).toBeInTheDocument();
    expect(screen.getByText("Delivery Channels")).toBeInTheDocument();
    expect(screen.getByText("Tuning")).toBeInTheDocument();
  });

  it("shows the Silo Push Relay channel status", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());

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

  it("configures the mail server inside the Email channel card", async () => {
    restartKeysMock.mockReturnValue(new Set(["email.smtp_host"]));
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "email.smtp_host": "smtp.example.com",
        "email.enabled": "true",
        // Readiness mirrors the server rule: no sender address, not ready.
        "email.from_address": "silo@example.com",
      }),
    );

    render(renderPage());

    expect(screen.getByText("Mail server set up")).toBeInTheDocument();
    expect(screen.queryByText("Mail server address")).not.toBeInTheDocument();

    await openChannel(EMAIL_CHANNEL);

    expect(screen.getByText("Send email from this server")).toBeInTheDocument();
    expect(screen.getByText("Mail server address")).toBeInTheDocument();
    expect(screen.getByText("Port")).toBeInTheDocument();
    expect(screen.getByText("Encryption")).toBeInTheDocument();
    expect(screen.getByText("Password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send test" })).toBeInTheDocument();
    // The restart badge comes from the compiled key list, not from hint text.
    expect(screen.getAllByLabelText("Takes effect after a server restart").length).toBe(1);
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

  it("configures the Discord application inside the Discord channel card", async () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "discord.client_id": "1234567890" }));

    render(renderPage());
    await openChannel(DISCORD_CHANNEL);

    expect(screen.queryByRole("link", { name: "Integrations tab" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Client ID")).toHaveValue("1234567890");
    expect(screen.getByText("Client secret")).toBeInTheDocument();
    expect(screen.getByText("Bot token")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Test bot token" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Show setup guide/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Invite bot to server/ })).toBeInTheDocument();
    // Delivery and appearance stay here.
    expect(screen.getByText("Artwork")).toBeInTheDocument();
    expect(screen.getByText("Let people pick a DM per episode")).toBeInTheDocument();
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

  it("counts the enabled channels on the pipeline card", () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());

    expect(screen.getByRole("heading", { level: 1, name: "Notifications" })).toBeInTheDocument();
    expect(screen.getByText("5/7 channels on")).toBeInTheDocument();
  });

  it("warns on the pipeline card when sending is paused", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "notifications.fanout_enabled": "false" }));

    render(renderPage());

    expect(
      screen.getByText("Sending is paused; new content waits in the queue."),
    ).toBeInTheDocument();
  });

  it("hides tuning and webhook limits behind Advanced disclosures", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());
    await openChannel(WEBHOOK_CHANNEL);

    expect(screen.queryByText("Settle window")).not.toBeInTheDocument();
    expect(screen.queryByText("Read notifications")).not.toBeInTheDocument();
    expect(screen.queryByText("Webhooks per person")).not.toBeInTheDocument();

    for (const toggle of screen.getAllByRole("button", { name: /Advanced · 3 settings/ })) {
      await userEvent.click(toggle);
    }

    expect(screen.getByText("Settle window")).toBeInTheDocument();
    expect(screen.getByText("Read notifications")).toBeInTheDocument();
    expect(screen.getByText("Webhooks per person")).toBeInTheDocument();
  });

  it("auto-expands an advanced disclosure that holds a staged change", async () => {
    const form = makeForm();
    form.isDirty = vi.fn((key: string) => key === "notifications.retention.read_days");
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());

    expect(screen.getByText("Read notifications")).toBeInTheDocument();
    expect(screen.queryByText("Settle window")).not.toBeInTheDocument();
  });
});
