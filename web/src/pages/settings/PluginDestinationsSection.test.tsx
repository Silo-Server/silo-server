import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { PluginDestinationsSection } from "./PluginDestinationsSection";

const state = vi.hoisted(() => ({
  etag: '"r1"',
  update: vi.fn(),
  remove: vi.fn(),
}));
vi.mock("@/api/v2/notifications", () => ({ notificationScope: () => "synthetic-profile" }));
vi.mock("@/hooks/queries/notifications", () => ({
  useNotificationPreferences: () => ({ data: undefined }),
}));
vi.mock("@/hooks/queries/notificationWebhooks", () => ({
  useNotificationCapability: () => ({
    isSuccess: true,
    data: { plugin_destinations: { available: true, max_per_profile: 10 } },
  }),
}));
vi.mock("@/hooks/queries/notificationPluginDestinations", () => ({
  pluginDestinationErrorMessage: (_: unknown, fallback: string) => fallback,
  useNotificationPluginChannels: () => ({
    data: [
      {
        plugin_id: "ntfy",
        capability_id: "push",
        display_name: "ntfy",
        description: "",
        settings_form: {
          json_schema: JSON.stringify({
            type: "object",
            properties: { topic: { type: "string" }, tag: { type: "string", minLength: 2 } },
          }),
          admin_form: {
            fields: [
              { key: "topic", label: "Topic", control: "TEXT", required: true, secret: false },
              { key: "tag", label: "Tag", control: "TEXT", required: false, secret: false },
            ],
          },
        },
      },
    ],
  }),
  useNotificationPluginDestinations: () => ({
    isLoading: false,
    data: [
      {
        id: "dest-1",
        etag: state.etag,
        name: "Phone",
        plugin_id: "ntfy",
        capability_id: "push",
        channel_name: "ntfy",
        available: true,
        enabled: true,
        settings: { topic: "alerts", tag: "tv" },
        secret_settings_set: [],
        notify_favorites: true,
        notify_watchlist: true,
        notify_continue_watching: true,
        notify_next_up: true,
        notify_requests: true,
        consecutive_failures: 0,
      },
    ],
  }),
  useCreateNotificationPluginDestination: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateNotificationPluginDestination: () => ({ mutate: state.update, isPending: false }),
  useDeleteNotificationPluginDestination: () => ({ mutate: state.remove, isPending: false }),
  useTestNotificationPluginDestination: () => ({ mutate: vi.fn(), isPending: false }),
}));

beforeEach(() => {
  state.etag = '"r1"';
  state.update.mockReset();
  state.remove.mockReset();
});
afterEach(cleanup);

it("sends a cleared optional setting as null so the plugin's default applies", () => {
  render(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.change(screen.getByLabelText("Tag"), { target: { value: "" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));

  expect(state.update).toHaveBeenCalledOnce();
  expect(state.update.mock.calls[0][0].settings).toEqual({ topic: "alerts", tag: null });
});

it("deletes with the validator seen when the confirmation opened", () => {
  const { rerender } = render(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  // The list refetches while the dialog is open.
  state.etag = '"r2"';
  rerender(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));

  expect(state.remove).toHaveBeenCalledOnce();
  expect(state.remove.mock.calls[0][0]).toEqual({ id: "dest-1", etag: '"r1"' });
});
