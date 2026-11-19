import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { V2ProblemError, type Problem } from "@/api/v2/request";
import { PluginDestinationsSection } from "./PluginDestinationsSection";

const state = vi.hoisted(() => ({
  etag: '"r1"',
  update: vi.fn(),
  remove: vi.fn(),
}));
// field is one flat destination form field, as the server describes it:
// optional members the plugin doesn't declare are absent.
function field(overrides: Record<string, unknown>) {
  return {
    control: "text",
    type: "string",
    required: false,
    secret: false,
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
vi.mock("@/hooks/queries/notificationPluginDestinations", async () => ({
  pluginDestinationErrorMessage: (_: unknown, fallback: string) => fallback,
  pluginSettingsFieldErrors: (
    await vi.importActual<typeof import("@/hooks/queries/notificationPluginDestinations")>(
      "@/hooks/queries/notificationPluginDestinations",
    )
  ).pluginSettingsFieldErrors,
  pluginDestinationNameError: (
    await vi.importActual<typeof import("@/hooks/queries/notificationPluginDestinations")>(
      "@/hooks/queries/notificationPluginDestinations",
    )
  ).pluginDestinationNameError,
  pluginSettingsRejection: (
    await vi.importActual<typeof import("@/hooks/queries/notificationPluginDestinations")>(
      "@/hooks/queries/notificationPluginDestinations",
    )
  ).pluginSettingsRejection,
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

it("shows the server's field errors under their fields and the plugin's refusal under the form", () => {
  state.update.mockImplementation((_input, options: { onError: (error: unknown) => void }) =>
    options.onError(
      new V2ProblemError("updateNotificationPluginDestination", {
        type: "https://silo.example.test/problems/validation_failed",
        title: "Validation failed",
        status: 422,
        detail: "The request did not pass validation; see errors.",
        errors: [
          {
            location: "body.settings.topic",
            code: "required",
            detail: "This setting is required.",
          },
          {
            location: "body.settings",
            code: "invalid",
            detail: "ntfy says the topic is reserved.",
          },
        ],
      } as Problem),
    ),
  );
  render(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));

  const topic = screen.getByLabelText(/Topic/);
  expect(topic.closest("div")?.parentElement?.textContent).toContain("This setting is required.");
  expect(screen.getByRole("alert").textContent).toBe("ntfy says the topic is reserved.");
});

it("shows a name that is already in use under the Name field", () => {
  state.update.mockImplementation((_input, options: { onError: (error: unknown) => void }) =>
    options.onError(
      new V2ProblemError("updateNotificationPluginDestination", {
        type: "https://silo.example.test/problems/validation_failed",
        title: "Validation failed",
        status: 422,
        detail: "The request did not pass validation; see errors.",
        errors: [
          {
            location: "body.name",
            code: "duplicate",
            detail: "A destination with this name already exists.",
          },
        ],
      } as Problem),
    ),
  );
  render(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));

  const alert = screen.getByRole("alert");
  expect(alert.textContent).toBe("A destination with this name already exists.");
  expect(screen.getByLabelText("Name").getAttribute("aria-describedby")).toBe(alert.id);
});

it("clears a saved optional secret on save, and typing a new value replaces it instead", () => {
  render(<PluginDestinationsSection />);
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  fireEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));

  expect(state.update).toHaveBeenCalledOnce();
  expect(state.update.mock.calls[0]?.[0]?.clear_secrets).toEqual(["token"]);
  expect(state.update.mock.calls[0]?.[0]?.settings).not.toHaveProperty("token");

  state.update.mockReset();
  fireEvent.click(screen.getByRole("button", { name: "Keep saved secret" }));
  fireEvent.click(screen.getByRole("button", { name: "Clear saved secret" }));
  fireEvent.change(screen.getByLabelText(/Token/), { target: { value: "fresh" } });
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(state.update.mock.calls[0]?.[0]?.clear_secrets).toBeUndefined();
  expect(state.update.mock.calls[0]?.[0]?.settings).toMatchObject({ token: "fresh" });
});
