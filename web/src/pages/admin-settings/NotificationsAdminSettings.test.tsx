import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
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
    // Mail readiness is read for the Email channel's status line; the mail
    // server itself is edited on the Email page.
    for (const key of ["email.enabled", "email.smtp_host", "email.from_address"]) {
      expect(keys).toContain(key);
    }
    for (const key of [
      "email.smtp_port",
      "email.smtp_security",
      "email.smtp_username",
      "email.smtp_password",
      "email.from_name",
    ]) {
      expect(keys).not.toContain(key);
    }
    for (const key of ["discord.client_id", "discord.client_secret", "discord.bot_token"]) {
      expect(keys).toContain(key);
    }

    expect(screen.getByText("Silo Push Relay")).toBeInTheDocument();
    expect(screen.getByText(/delivered by APNs or FCM/)).toBeInTheDocument();
    expect(screen.getByText("Relay configured")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Silo Push Relay/ }));

    expect(screen.getByText("Privacy disclosure")).toBeInTheDocument();
    // Per-platform switches and relay internals wait under the card's Advanced.
    expect(screen.queryByText("Apple Push (APNs)")).not.toBeInTheDocument();
    expect(screen.queryByText("Relay URL")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Advanced · 4 settings" }));
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

  it("treats an unregistered relay as normal: the server registers on its first send", async () => {
    // A fresh install: delivery is on by default and nothing is registered.
    const form = makeForm({
      "notifications.push_relay_deployment_id": "",
      "notifications.push_relay_key_prefix": "",
      "notifications.push_relay_expires_at": "",
    });
    form.sensitiveConfigured = [];
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());

    const chip = screen.getByText("Registers on first send");
    expect(chip).not.toHaveClass("text-amber-500");
    expect(screen.queryByText(/registration required/i)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Silo Push Relay/ }));
    await userEvent.click(screen.getByRole("button", { name: "Advanced · 4 settings" }));
    expect(screen.getByText(/Silo registers on its first push/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Register relay" })).toBeInTheDocument();
  });

  it("keeps the warning and opens the relay controls when re-registration is required", async () => {
    // Parked by an admin's clear or the relay: first-use registration will not
    // undo that, so the admin has to act.
    const form = makeForm({
      "notifications.push_relay_reregistration_required": "true",
      "notifications.push_relay_deployment_id": "",
    });
    form.sensitiveConfigured = [];
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());

    expect(screen.getByText("Re-registration required")).toHaveClass("text-amber-500");
    await userEvent.click(screen.getByRole("button", { name: /Silo Push Relay/ }));
    expect(screen.getByRole("button", { name: "Re-register relay" })).toBeInTheDocument();
  });

  it("links the Email channel to the Email page instead of holding the mail server", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());
    // The channel defaults on while mail is not set up, which is every fresh
    // install: that is a neutral state, not a warning.
    expect(screen.getByText("Mail server not set up")).not.toHaveClass("text-amber-500");
    await openChannel(EMAIL_CHANNEL);

    expect(screen.getByRole("link", { name: "Email page" })).toHaveAttribute(
      "href",
      "/admin/settings/email",
    );
    expect(screen.getByText(/No email can send until it is/)).toBeInTheDocument();
    for (const label of [
      "Send email from this server",
      "From address",
      "Mail server address",
      "Port",
      "Encryption",
      "Username",
      "Password",
    ]) {
      expect(screen.queryByLabelText(label)).not.toBeInTheDocument();
    }
    expect(screen.queryByRole("button", { name: "Send test" })).not.toBeInTheDocument();
    expect(
      screen.getByRole("switch", { name: "Let people pick an email per episode" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Daily summary hour")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Advanced · 1 setting" }));
    expect(screen.getByRole("spinbutton", { name: "Daily summary hour" })).toHaveValue(8);
  });

  it("says when the mail server is set up", async () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "email.enabled": "true",
        "email.smtp_host": "smtp.example.com",
        "email.from_address": "silo@example.com",
      }),
    );

    render(renderPage());
    expect(screen.getByText("Mail server set up")).toBeInTheDocument();
    await openChannel(EMAIL_CHANNEL);
    expect(screen.getByText(/Mail server set up\. Change it on the/)).toBeInTheDocument();
  });

  it("files Discord artwork and the summary hour under the card's Advanced", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());
    await openChannel(DISCORD_CHANNEL);

    expect(
      screen.getByRole("switch", { name: "Let people pick a DM per episode" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Artwork")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Advanced · 2 settings" }));
    expect(screen.getByRole("combobox", { name: "Artwork" })).toHaveTextContent(
      "Provider images only",
    );
    expect(screen.getByRole("spinbutton", { name: "Daily summary hour" })).toHaveValue(8);
  });

  it("opens the Discord setup guide through the shared disclosure", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());
    await openChannel(DISCORD_CHANNEL);

    const guide = screen.getByRole("button", { name: "Setup guide" });
    expect(guide).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(/discord.com\/developers\/applications/)).not.toBeInTheDocument();
    await userEvent.click(guide);
    expect(screen.getByText(/discord.com\/developers\/applications/)).toBeInTheDocument();
  });

  it("keeps server channel destinations visible and their tuning under Advanced", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());
    await userEvent.click(screen.getByRole("button", { name: /Server-wide announcements/ }));

    expect(screen.getByRole("button", { name: "Add server channel" })).toBeInTheDocument();
    expect(screen.queryByText("Batch window")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Advanced · 2 settings" }));
    expect(screen.getByRole("spinbutton", { name: "Batch window" })).toHaveValue(300);
    expect(
      screen.getByRole("switch", { name: "Mention the requester on Discord" }),
    ).toBeInTheDocument();
  });

  it("says the webhook delivery limit is counted per server process", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());
    await userEvent.click(screen.getByRole("button", { name: /Webhooks people create/ }));
    await userEvent.click(screen.getByRole("button", { name: "Advanced · 3 settings" }));

    expect(screen.getByText(/Counted by each server process on its own/)).toBeInTheDocument();
  });

  it("gathers the pipeline switches, grouping, and retention in one Advanced area", async () => {
    const form = makeForm();
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());

    expect(screen.queryByText("Pipeline")).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Notice new content" })).not.toBeInTheDocument();
    const processing = screen.getByRole("group", { name: "Processing" });
    await userEvent.click(
      within(processing).getByRole("button", { name: "Advanced · 8 settings" }),
    );

    for (const name of ["Notice new content", "Work out who wants it"]) {
      expect(within(processing).getByRole("switch", { name })).toBeChecked();
    }
    for (const name of [
      "Settle window",
      "Max messages per show",
      "Max content age",
      "Read notifications",
      "Unread notifications",
      "Sent history",
    ]) {
      expect(within(processing).getByRole("spinbutton", { name })).toBeInTheDocument();
    }
    await userEvent.click(within(processing).getByRole("switch", { name: "Notice new content" }));
    expect(form.setValue).toHaveBeenCalledWith("notifications.release_events_enabled", "false");
  });

  it("opens the Advanced area for a staged edit inside it", () => {
    const form = makeForm();
    form.isDirty = vi.fn((key: string) => key === "notifications.retention.read_days");
    useSettingsFormMock.mockReturnValue(form);

    render(renderPage());

    expect(screen.getByRole("spinbutton", { name: "Read notifications" })).toBeInTheDocument();
  });

  it("says so up top, and opens the switches, when the pipeline is paused", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "notifications.fanout_enabled": "false" }));

    render(renderPage());

    expect(screen.getByRole("status")).toHaveTextContent(/Sending is paused/);
    expect(screen.getByRole("switch", { name: "Work out who wants it" })).not.toBeChecked();
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
