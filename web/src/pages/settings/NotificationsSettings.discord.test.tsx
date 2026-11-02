import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import NotificationsSettings from "./NotificationsSettings";

const state = vi.hoisted(() => ({
  profile: { is_primary: true } as { is_primary: boolean } | null,
  linked: true,
  mutate: vi.fn(),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: state.profile }),
}));
vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({}),
  isCapturedProfileAuthorityActive: () => true,
}));
vi.mock("@/api/v2/notifications", () => ({
  notificationScope: () => "synthetic-profile",
  requireNotificationAuthority: () => ({}),
  captureNotificationAuthority: () => ({}),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/queries/notifications", () => ({
  useNotificationPreferences: () => ({ data: undefined }),
  useEmailNotificationPreferences: () => ({ data: undefined }),
  useDiscordNotificationPreferences: () => ({
    data: { linked: state.linked, discord_username: "Synthetic recipient", mode: "daily_digest" },
  }),
  ...Object.fromEntries(
    [
      "useClearEmailNotificationAddress",
      "useDiscordLinkInit",
      "useRequestEmailNotificationAddress",
      "useUnlinkDiscord",
      "useUpdateDiscordNotificationPreferences",
      "useUpdateEmailNotificationPreferences",
      "useUpdateNotificationPreferences",
    ].map((name) => [name, () => ({ mutate: state.mutate, isPending: false })]),
  ),
}));
vi.mock("@/hooks/queries/notificationWebhooks", () => ({
  useNotificationCapability: () => ({
    data: {
      webhooks: { available: false },
      discord: { available: true, modes: ["daily_digest"], digest_hour: 8 },
    },
  }),
  useNotificationWebhooks: () => ({ data: [] }),
  useWebPushSubscriptions: () => ({ data: [] }),
  ...Object.fromEntries(
    [
      "useCreateNotificationWebhook",
      "useDeleteNotificationWebhook",
      "useDeleteWebPushSubscription",
      "useRotateNotificationWebhookSecret",
      "useTestNotificationWebhook",
      "useUpdateNotificationWebhook",
    ].map((name) => [name, () => ({ mutate: state.mutate, isPending: false })]),
  ),
}));
function page() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <NotificationsSettings />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return within(screen.getByRole("group", { name: "Discord Notifications" }));
}
beforeEach(() => {
  state.profile = { is_primary: true };
  state.linked = true;
  state.mutate.mockReset();
  vi.stubGlobal(
    "fetch",
    vi.fn(() => {
      throw new Error("Provider transport is disabled in this offline test");
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it.each([false, null])("keeps Discord controls read-only for primary=%s", (primary) => {
  state.profile = primary === null ? null : { is_primary: primary };
  const section = page();
  expect(section.getByText(/Switch to the primary profile/)).toBeInTheDocument();
  expect(section.getByRole("switch")).toBeDisabled();
  expect(section.getByRole("button", { name: "Unlink" })).toBeDisabled();
  expect(section.getByRole("combobox")).toBeDisabled();
  fireEvent.click(section.getByRole("button", { name: "Unlink" }));
  expect(state.mutate).not.toHaveBeenCalled();
});
it("keeps link initiation read-only for a secondary profile", () => {
  state.profile = { is_primary: false };
  state.linked = false;
  const section = page();
  expect(section.getByRole("button", { name: "Link Discord" })).toBeDisabled();
  fireEvent.click(section.getByRole("button", { name: "Link Discord" }));
  expect(state.mutate).not.toHaveBeenCalled();
});
it("lets the primary profile unlink and change the account mode", () => {
  const section = page();
  expect(section.queryByText(/Switch to the primary profile/)).not.toBeInTheDocument();
  expect(section.getByRole("combobox")).toBeEnabled();
  fireEvent.click(section.getByRole("switch"));
  expect(state.mutate).toHaveBeenCalledWith({ mode: "off" });
  fireEvent.click(section.getByRole("button", { name: "Unlink" }));
  expect(state.mutate).toHaveBeenCalledTimes(2);
});
