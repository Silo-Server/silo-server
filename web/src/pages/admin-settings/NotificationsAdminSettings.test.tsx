import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

import NotificationsAdminSettings from "./NotificationsAdminSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/queries/admin/serverNotificationChannels", () => ({
  useServerNotificationChannels: () => ({ data: [] }),
}));

function makeForm() {
  return {
    isLoading: false,
    getValue: (key: string) => {
      switch (key) {
        case "notifications.release_events_enabled":
        case "notifications.fanout_enabled":
        case "notifications.ui_enabled":
        case "notifications.web_push_enabled":
        case "notifications.apple_push_delivery_enabled":
          return "true";
        case "notifications.push_relay_url":
          return "https://push.siloserver.org";
        case "notifications.push_relay_deployment_id":
          return "01DEPLOYMENT";
        case "notifications.push_relay_key_prefix":
          return "cap_v1_test";
        case "notifications.push_relay_expires_at":
          return "2026-08-10T00:00:00Z";
        default:
          return "";
      }
    },
    setValue: vi.fn(),
    dirtyCount: 0,
    dirtyKeys: [],
    isDirty: vi.fn(() => false),
    save: vi.fn(),
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
      <MemoryRouter initialEntries={["/admin/settings?tab=notifications"]}>
        <NotificationsAdminSettings />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function renderStaticPage() {
  return renderToStaticMarkup(renderPage());
}

describe("NotificationsAdminSettings", () => {
  it("registers Silo Push Relay settings with the shared settings form", () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    renderStaticPage();

    expect(useSettingsFormMock).toHaveBeenCalledWith({
      keys: expect.arrayContaining([
        "notifications.apple_push_delivery_enabled",
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

  it("shows the Silo Push Relay channel status", async () => {
    useSettingsFormMock.mockReturnValue(makeForm());

    render(renderPage());

    expect(screen.getByText("Silo Push Relay")).toBeInTheDocument();
    expect(screen.getByText(/Mobile push delivery through Silo's relay/)).toBeInTheDocument();
    expect(screen.getByText(/Android support will use the same relay/)).toBeInTheDocument();
    expect(screen.getByText("Relay configured")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Silo Push Relay/ }));

    expect(screen.getByText("Privacy disclosure")).toBeInTheDocument();
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
});
