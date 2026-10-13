import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RestartKeyMatcher } from "@/hooks/useRestartKeys";

import LoggingSettings from "./LoggingSettings";
import {
  DEFAULT_BUCKET_POLICIES,
  OPSLOG_BUCKET_POLICIES_KEY,
  serializeBucketPolicies,
} from "./logRetentionPolicy";

// Radix Select opens through pointer capture and scrolls its options into
// view, neither of which jsdom provides.
window.HTMLElement.prototype.hasPointerCapture ??= () => false;
window.HTMLElement.prototype.setPointerCapture ??= () => {};
window.HTMLElement.prototype.releasePointerCapture ??= () => {};
window.HTMLElement.prototype.scrollIntoView ??= () => {};

const settingsFormMock = vi.fn();

// Most cases drive the page from a hand-written form; the ones that prove a
// value reaches the server flip this and run the real hook instead.
const { realForm, serverSettings, updateSettingsMock } = vi.hoisted(() => ({
  realForm: { enabled: false },
  serverSettings: { current: {} as Record<string, string> },
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
  useAdminServerSettings: () => ({ data: serverSettings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({
    data: { configured: [], managed_by_env: [] },
    isError: false,
  }),
  useUpdateServerSettings: () => ({ mutateAsync: updateSettingsMock, isPending: false }),
}));

// Mirrors the server registry: nothing on this page needs a restart. Tests that
// need a different registry override the mock.
const NO_RESTART: RestartKeyMatcher = { has: () => false };
const restartKeysMock = vi.fn((): RestartKeyMatcher => NO_RESTART);

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => restartKeysMock(),
}));

const METADATA_RULE = JSON.stringify([
  { component: "metadata", level: "info", retention_days: 1, max_rows: 100, max_size_mb: 8 },
]);

function mockForm(overrides: Partial<Record<string, unknown>> = {}) {
  const form = {
    isLoading: false,
    getValue: () => "",
    getPersistedValue: () => "",
    setValue: vi.fn(),
    resetValue: vi.fn(),
    dirtyCount: 0,
    dirtyKeys: [],
    isDirty: () => false,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
    ...overrides,
  };
  settingsFormMock.mockReturnValue(form);
  return form;
}

function group(name: string) {
  return within(screen.getByRole("group", { name }));
}

async function openAdvanced(name: string) {
  await userEvent.click(group(name).getByRole("button", { name: /Advanced/ }));
}

describe("LoggingSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    settingsFormMock.mockReset();
    restartKeysMock.mockReset();
    restartKeysMock.mockReturnValue(NO_RESTART);
  });

  it("manages the server log and log retention keys on one save bar", () => {
    mockForm();
    render(<LoggingSettings />);

    expect(settingsFormMock.mock.calls[0]?.[0]?.keys).toEqual([
      "server.log_level",
      "server.log_quiet",
      "opslog.retention_days",
      "opslog.max_rows",
      "opslog.max_size_mb",
      "policy.decision_log_retention_days",
      "policy.decision_log_verbosity",
      "policy.decision_log_scope_sample_rate",
      OPSLOG_BUCKET_POLICIES_KEY,
    ]);
    expect(screen.getByRole("heading", { name: "Logging" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent)).toEqual([
      "Server log",
      "Log retention",
    ]);
  });

  it("says the log level is the container's, not the Server Log page's", () => {
    mockForm();
    render(<LoggingSettings />);

    // The Server Log page captures at opslog.capture_level (Info), whatever
    // server.log_level says.
    expect(
      group("Server log").getByText(/The Server Log page records Info and above either way/),
    ).toBeInTheDocument();
  });

  it("stages the server log level and quiet prefixes", async () => {
    const form = mockForm({
      getValue: (key: string) => (key === "server.log_level" ? "info" : ""),
    });
    render(<LoggingSettings />);

    await userEvent.click(group("Server log").getByRole("combobox", { name: "Log level" }));
    await userEvent.click(screen.getByRole("option", { name: "Debug" }));
    expect(form.setValue).toHaveBeenCalledWith("server.log_level", "debug");

    await openAdvanced("Server log");
    await userEvent.type(group("Server log").getByLabelText("Quiet log prefixes"), "m");
    expect(form.setValue).toHaveBeenCalledWith("server.log_quiet", "m");
  });

  it("stages the retention limits and permission-check settings", async () => {
    const form = mockForm();
    render(<LoggingSettings />);
    const retention = group("Log retention");

    // Only the age limit shows by default; the row and size caps are backstops.
    expect(retention.queryByLabelText("Maximum log entries")).not.toBeInTheDocument();
    expect(retention.queryByLabelText("Maximum log size")).not.toBeInTheDocument();
    expect(retention.getByRole("button", { name: "Advanced · 6 settings" })).toBeInTheDocument();
    await userEvent.type(retention.getByLabelText("Delete log entries older than"), "9");
    expect(form.setValue).toHaveBeenCalledWith("opslog.retention_days", "9");

    await openAdvanced("Log retention");
    await userEvent.type(retention.getByLabelText("Maximum log entries"), "9");
    expect(form.setValue).toHaveBeenCalledWith("opslog.max_rows", "9");
    await userEvent.type(retention.getByLabelText("Maximum log size"), "9");
    expect(form.setValue).toHaveBeenCalledWith("opslog.max_size_mb", "9");
    expect(retention.getByText("Permission checks")).toBeInTheDocument();
    await userEvent.type(retention.getByLabelText("Delete permission records older than"), "9");
    expect(form.setValue).toHaveBeenCalledWith("policy.decision_log_retention_days", "9");
    await userEvent.type(retention.getByLabelText("Record one allowed check in every"), "9");
    expect(form.setValue).toHaveBeenCalledWith("policy.decision_log_scope_sample_rate", "9");
    await userEvent.click(retention.getByRole("combobox", { name: "How much to record" }));
    await userEvent.click(screen.getByRole("option", { name: "Full" }));
    expect(form.setValue).toHaveBeenCalledWith("policy.decision_log_verbosity", "verbose");
  });

  it("opens the retention Advanced section while a log cap is unsaved", () => {
    mockForm({
      isDirty: (key: string) => key === "opslog.max_size_mb",
      dirtyCount: 1,
      getValue: (key: string) => (key === "opslog.max_size_mb" ? "512" : ""),
    });
    render(<LoggingSettings />);

    expect(group("Log retention").getByLabelText("Maximum log size")).toHaveValue(512);
  });

  it("stages bucket override edits through the shared save model", async () => {
    const setValue = vi.fn();
    mockForm({
      setValue,
      isDirty: (key: string) => key === OPSLOG_BUCKET_POLICIES_KEY,
      dirtyCount: 1,
      getValue: (key: string) => (key === OPSLOG_BUCKET_POLICIES_KEY ? METADATA_RULE : ""),
    });

    render(<LoggingSettings />);

    // A dirty rule set holds the Advanced section open on its own.
    await userEvent.click(screen.getByRole("button", { name: /Remove metadata rule/ }));

    expect(setValue).toHaveBeenCalledWith(OPSLOG_BUCKET_POLICIES_KEY, "[]");
  });

  it("restores the recommended rules and adds a blank one", async () => {
    const setValue = vi.fn();
    mockForm({ setValue });
    render(<LoggingSettings />);
    await openAdvanced("Log retention");

    expect(screen.getByText("No per-area rules configured.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Restore Recommended Rules" }));
    expect(setValue).toHaveBeenCalledWith(
      OPSLOG_BUCKET_POLICIES_KEY,
      serializeBucketPolicies(DEFAULT_BUCKET_POLICIES),
    );

    // The blank row has no component yet, so it serializes to nothing until
    // the admin names one.
    await userEvent.click(screen.getByRole("button", { name: "Add Rule" }));
    expect(setValue).toHaveBeenLastCalledWith(OPSLOG_BUCKET_POLICIES_KEY, "[]");
  });

  it("falls back to the recommended rules when the saved ones cannot be read", async () => {
    mockForm({
      getValue: (key: string) => (key === OPSLOG_BUCKET_POLICIES_KEY ? "{not json" : ""),
    });
    render(<LoggingSettings />);
    await openAdvanced("Log retention");

    expect(screen.getByText(/The saved rules could not be read/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /Remove (metadata|scanner) rule/ })).toHaveLength(
      DEFAULT_BUCKET_POLICIES.length,
    );
  });

  it("marks a field for a restart on its own when the registry names it", async () => {
    restartKeysMock.mockReturnValue({ has: (key: string) => key === "opslog.max_rows" });
    mockForm();
    render(<LoggingSettings />);
    await openAdvanced("Log retention");

    expect(screen.getAllByLabelText("Takes effect after a server restart")).toHaveLength(1);
    const badges = group("Log retention").getAllByLabelText("Takes effect after a server restart");
    expect(badges[0]?.parentElement).toHaveTextContent("Maximum log entries");
  });

  it("saves and discards through the save bar", async () => {
    const form = mockForm({ dirtyCount: 2 });
    render(<LoggingSettings />);

    expect(screen.getByText("2 unsaved changes")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(form.discard).toHaveBeenCalledTimes(1);
  });

  describe("saving to the server", () => {
    beforeEach(() => {
      realForm.enabled = true;
      serverSettings.current = {
        "server.log_level": "info",
        "server.log_quiet": "",
        "opslog.retention_days": "7",
        "opslog.max_rows": "1000000",
        "opslog.max_size_mb": "1024",
        [OPSLOG_BUCKET_POLICIES_KEY]: METADATA_RULE,
      };
      updateSettingsMock.mockReset();
      updateSettingsMock.mockResolvedValue({ values: {}, restart_required: false });
    });

    afterEach(() => {
      realForm.enabled = false;
    });

    it("sends the edited log keys in one batch", async () => {
      render(<LoggingSettings />);

      const days = group("Log retention").getByLabelText("Delete log entries older than");
      await userEvent.clear(days);
      await userEvent.type(days, "14");
      await openAdvanced("Server log");
      await userEvent.type(group("Server log").getByLabelText("Quiet log prefixes"), "scanner");
      await openAdvanced("Log retention");
      await userEvent.click(screen.getByRole("button", { name: /Remove metadata rule/ }));

      expect(screen.getByText("3 unsaved changes")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({
          "opslog.retention_days": "14",
          "server.log_quiet": "scanner",
          [OPSLOG_BUCKET_POLICIES_KEY]: "[]",
        }),
      );
    });
  });
});
