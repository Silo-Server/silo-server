import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { PluginDestinationsSection } from "./PluginDestinationsSection";

const state = vi.hoisted(() => ({
  etag: '"r1"',
  update: vi.fn(),
  remove: vi.fn(),
}));
// field is one flat destination form field, as the server describes it.
function field(overrides: Record<string, unknown>) {
  return {
    description: "",
    placeholder: "",
    control: "text",
    type: "string",
    required: false,
    secret: false,
    options: [],
    minimum: null,
    maximum: null,
    min_length: null,
    max_length: null,
    pattern: "",
    ...overrides,
  };
}

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
          fields: [
            field({ key: "topic", label: "Topic", required: true }),
            field({ key: "tag", label: "Tag", min_length: 2 }),
            field({ key: "priority", label: "Priority", control: "number", type: "integer" }),
            field({ key: "token", label: "Token", control: "password", secret: true }),
          ],
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
        settings: { topic: "alerts", tag: "tv", priority: 3 },
        secret_settings_set: ["token"],
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

it("sends settings typed as the form declares, keeps a blank stored secret, and clears with null", () => {
  render(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.change(screen.getByLabelText("Tag"), { target: { value: "" } });
  fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "4" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));

  expect(state.update).toHaveBeenCalledOnce();
  // The stored token isn't sent, so it stays as saved.
  expect(state.update.mock.calls[0]?.[0]?.settings).toEqual({
    topic: "alerts",
    tag: null,
    priority: 4,
  });
});

it("deletes with the validator seen when the confirmation opened", () => {
  const { rerender } = render(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  // The list refetches while the dialog is open.
  state.etag = '"r2"';
  rerender(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));

  expect(state.remove).toHaveBeenCalledOnce();
  expect(state.remove.mock.calls[0]?.[0]).toEqual({ id: "dest-1", etag: '"r1"' });
});
