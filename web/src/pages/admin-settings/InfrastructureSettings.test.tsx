import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import InfrastructureSettings from "./InfrastructureSettings";
import { OPSLOG_BUCKET_POLICIES_KEY } from "./logRetentionPolicy";

const settingsFormMock = vi.fn();
const useCheckAdminSettingsConnectionMock = vi.fn();

// Most cases drive the page from a hand-written form so a single render can
// describe any staged state. The cases that have to prove a value reaches the
// server flip this and run the real hook instead.
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

// Mirrors the server registry's restart prefixes: every s3/redis/database/
// userdb key restarts; nothing else on this page does. Tests that need a
// different registry override the mock.
const restartKeysMock = vi.fn(() => ({
  has: (key: string) => /^(s3|redis|database|userdb)\./.test(key),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => restartKeysMock(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: (...args: unknown[]) =>
    useCheckAdminSettingsConnectionMock(...args),
  useAdminServerSettings: () => ({ data: serverSettings.current, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: sensitiveStatus.current, isError: false }),
  useUpdateServerSettings: () => ({ mutateAsync: updateSettingsMock, isPending: false }),
}));

useCheckAdminSettingsConnectionMock.mockReturnValue({ isPending: false, mutateAsync: vi.fn() });

type FormOverrides = Partial<Record<string, unknown>>;

function mockForm(overrides: FormOverrides = {}) {
  const form = {
    isLoading: false,
    getValue: (key: string) => (key === "s3.public_url_auth" ? "presigned" : ""),
    setValue: vi.fn(),
    resetValue: vi.fn(),
    dirtyCount: 0,
    dirtyKeys: [],
    isDirty: () => false,
    isClearStaged: () => false,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
    sensitiveStatusReady: true,
    sensitiveStatusError: false,
    buildConnectionCheckRequest: vi.fn(),
    ...overrides,
  };
  settingsFormMock.mockReturnValue(form);
  return form;
}

describe("InfrastructureSettings", () => {
  it("renders every field group heading", () => {
    mockForm();

    const markup = renderToStaticMarkup(<InfrastructureSettings />);

    for (const heading of ["Redis", "Public storage", "Private storage", "Database", "Logs"]) {
      expect(markup).toContain(heading);
    }
  });

  it("renders the page header on its own, with no description or status strip", () => {
    mockForm();

    render(<InfrastructureSettings />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Storage & Database" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Where Silo keeps its data. Changes here take effect after a restart."),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("Redis not configured")).not.toBeInTheDocument();
    expect(screen.queryByText("No public bucket set")).not.toBeInTheDocument();
  });

  it("claims a restart only for the groups whose settings actually need one", () => {
    mockForm();

    render(<InfrastructureSettings />);

    // No page-wide claim: the Logs group applies live, and a blanket line
    // would tell an admin to restart for it.
    expect(
      screen.queryByText("Changes on this page apply after a restart."),
    ).not.toBeInTheDocument();
    // Redis, both storage buckets, and Database each say it once; their
    // fields drop the per-field chips.
    expect(screen.getAllByText(/Changes apply after a restart/)).toHaveLength(4);
    expect(screen.queryAllByLabelText("Takes effect after a server restart")).toHaveLength(0);
    const logsGroup = within(screen.getByRole("group", { name: "Logs" }));
    expect(logsGroup.queryByText(/Changes apply after a restart/)).not.toBeInTheDocument();
  });

  it("demotes a group to per-field chips when one of its keys stops needing a restart", () => {
    // The same page against a future registry where the Redis URL hot-reloads:
    // the group-level claim must disappear on its own rather than stay wrong.
    restartKeysMock.mockReturnValueOnce({
      has: (key: string) => /^(s3|database|userdb)\./.test(key),
    });
    mockForm();

    render(<InfrastructureSettings />);

    const redisGroup = within(screen.getByRole("group", { name: "Redis" }));
    expect(redisGroup.queryByText(/Changes apply after a restart/)).not.toBeInTheDocument();
  });

  it("says what each bucket holds", () => {
    mockForm();

    render(<InfrastructureSettings />);

    expect(
      screen.getByText(
        /Files clients download directly: cached artwork, uploaded posters, and branding images/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /Files only the server reads: profile avatars, diagnostics bundles, and catalog seed artifacts/,
      ),
    ).toBeInTheDocument();
  });

  it("puts units beside the control rather than in the label", () => {
    mockForm();

    const markup = renderToStaticMarkup(<InfrastructureSettings />);

    expect(markup).toContain("Delete log entries older than");
    expect(markup).not.toContain("Delete log entries older than (days)");
    expect(markup).not.toContain("Maximum log size (MB)");
  });

  it("manages the merged database, storage and log keys in one form", () => {
    mockForm();

    renderToStaticMarkup(<InfrastructureSettings />);

    const calls = settingsFormMock.mock.calls as [{ keys: string[] }][];
    const keys = calls[calls.length - 1]?.[0].keys ?? [];
    expect(keys).toEqual(expect.arrayContaining(["redis.url", "database.max_connections"]));
    expect(keys).toEqual(
      expect.arrayContaining(["s3.public_bucket", "s3.private_bucket", OPSLOG_BUCKET_POLICIES_KEY]),
    );
    // Artwork storage lives on Library & Metadata, beside the rest of the
    // metadata behavior; this page only owns the buckets it writes into.
    expect(keys).not.toContain("metadata.cache_images");
    // The disabled Litestream storage tab is gone; its keys keep working through the API.
    expect(keys.filter((key) => key.startsWith("s3.user_db_"))).toEqual([]);
  });

  it("shows only essential controls until Advanced is opened", () => {
    mockForm();

    const markup = renderToStaticMarkup(<InfrastructureSettings />);

    expect(markup).toContain("Use Redis");
    expect(markup).toContain("Endpoint");
    expect(markup).toContain("Bucket");
    expect(markup).toContain("Check Connection");
    expect(markup).toContain("Maximum log entries");
    // Advanced, so not rendered while collapsed.
    expect(markup).not.toContain("Region");
    expect(markup).not.toContain("Maximum Postgres connections");
    expect(markup).not.toContain("Record one allowed check in every");
    expect(markup).not.toContain("Per-area limits");
    // Removed entirely.
    expect(markup).not.toContain("User DB");
    expect(markup).not.toContain("Not currently in use");
  });

  it("keeps the Redis connection check available when REDIS_URL comes from the environment", () => {
    mockForm({
      sensitiveConfigured: ["redis.url"],
      sensitiveManagedByEnv: ["redis.url"],
    });

    render(<InfrastructureSettings />);

    const redisGroup = within(screen.getByRole("group", { name: "Redis" }));
    // The value stays read-only — only writes are refused for env-managed keys —
    // but the check runs against the value the server merged from REDIS_URL.
    expect(redisGroup.getByLabelText("Connection URL")).toBeDisabled();
    expect(redisGroup.getByRole("button", { name: "Check Connection" })).toBeEnabled();
  });

  it("renders Check Connection as a filled button rather than flat text", () => {
    mockForm();

    render(<InfrastructureSettings />);

    for (const button of screen.getAllByRole("button", { name: "Check Connection" })) {
      expect(button).toHaveAttribute("data-variant", "secondary");
    }
  });

  it("opens an Advanced section while one of its fields is unsaved", () => {
    mockForm({ isDirty: (key: string) => key === "database.max_connections", dirtyCount: 1 });

    const markup = renderToStaticMarkup(<InfrastructureSettings />);

    expect(markup).toContain("Maximum Postgres connections");
    expect(markup).toContain("Advanced · 2 settings");
  });

  it("warns about the artwork cache when a public storage identity field is edited", () => {
    mockForm({ isDirty: (key: string) => key === "s3.public_bucket", dirtyCount: 1 });

    const markup = renderToStaticMarkup(<InfrastructureSettings />);

    expect(markup).toContain("Storage location change");
    expect(markup).toContain("will not change artwork cache records");
  });

  it("keeps a saved credential when its input is emptied", async () => {
    const form = mockForm({
      sensitiveConfigured: ["s3.public_access_key", "s3.public_secret_key"],
      getValue: (key: string) =>
        key === "s3.public_access_key" ? "draft" : key === "s3.public_url_auth" ? "presigned" : "",
    });

    render(<InfrastructureSettings />);

    // No Replace step: the saved credential is a masked, always-editable input.
    const publicGroup = within(screen.getByRole("group", { name: "Public storage" }));
    const input = publicGroup.getByLabelText("Access Key");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAttribute("placeholder", "••••••••••••");
    expect(publicGroup.queryByRole("button", { name: /Replace/ })).not.toBeInTheDocument();

    // Deleting the draft means "keep the saved secret", never "clear it".
    await userEvent.clear(input);
    expect(form.resetValue).toHaveBeenCalledWith("s3.public_access_key");
    expect(form.setValue).not.toHaveBeenCalledWith("s3.public_access_key", "");
  });

  it("keeps the credential input editable when saving fails", async () => {
    mockForm({
      sensitiveConfigured: ["s3.private_access_key"],
      dirtyCount: 1,
      save: vi.fn().mockRejectedValue(new Error("save failed")),
    });

    render(<InfrastructureSettings />);

    const privateGroup = within(screen.getByRole("group", { name: "Private storage" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(privateGroup.getByLabelText("Access Key")).toHaveAttribute("type", "password"),
    );
  });

  it("fails closed when protected credential status cannot be loaded", () => {
    mockForm({ sensitiveStatusReady: false, sensitiveStatusError: true });

    render(<InfrastructureSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Protected credential status is unavailable",
    );
    expect(screen.queryByLabelText("Access Key")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Secret Key")).not.toBeInTheDocument();
  });

  it("stages bucket override edits through the shared save model", async () => {
    const setValue = vi.fn();
    mockForm({
      setValue,
      isDirty: (key: string) => key === OPSLOG_BUCKET_POLICIES_KEY,
      dirtyCount: 1,
      getValue: (key: string) => {
        if (key === "s3.public_url_auth") return "presigned";
        if (key === OPSLOG_BUCKET_POLICIES_KEY) {
          return JSON.stringify([
            {
              component: "metadata",
              level: "info",
              retention_days: 1,
              max_rows: 100,
              max_size_mb: 8,
            },
          ]);
        }
        return "";
      },
    });

    render(<InfrastructureSettings />);

    await userEvent.click(screen.getByRole("button", { name: /Remove metadata rule/ }));

    expect(setValue).toHaveBeenCalledWith(OPSLOG_BUCKET_POLICIES_KEY, "[]");
  });

  describe("clearing a stored credential", () => {
    beforeEach(() => {
      realForm.enabled = true;
      serverSettings.current = { "s3.public_url_auth": "presigned" };
      sensitiveStatus.current = { configured: ["s3.public_access_key"], managed_by_env: [] };
      updateSettingsMock.mockReset();
      updateSettingsMock.mockResolvedValue({ values: {}, restart_required: false });
    });

    afterEach(() => {
      realForm.enabled = false;
    });

    it("stages the clear in the save bar and saves it as an empty value", async () => {
      render(<InfrastructureSettings />);

      const publicGroup = within(screen.getByRole("group", { name: "Public storage" }));
      await userEvent.click(publicGroup.getByRole("button", { name: "Clear saved value" }));

      expect(screen.getByText("1 unsaved change")).toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({ "s3.public_access_key": "" }),
      );
    });

    it("takes the clear back out of the save bar when the saved value is kept", async () => {
      render(<InfrastructureSettings />);

      const publicGroup = within(screen.getByRole("group", { name: "Public storage" }));
      await userEvent.click(publicGroup.getByRole("button", { name: "Clear saved value" }));
      await userEvent.click(publicGroup.getByRole("button", { name: "Keep saved value" }));

      expect(screen.queryByText("1 unsaved change")).not.toBeInTheDocument();
      expect(publicGroup.getByLabelText("Access Key")).toHaveAttribute(
        "placeholder",
        "••••••••••••",
      );
    });

    it("leaves an env-managed credential without a clear action", async () => {
      serverSettings.current = { "redis.url": "" };
      sensitiveStatus.current = {
        configured: ["redis.url"],
        managed_by_env: ["redis.url"],
      };

      render(<InfrastructureSettings />);

      const redisGroup = within(screen.getByRole("group", { name: "Redis" }));
      expect(redisGroup.getByLabelText("Connection URL")).toBeDisabled();
      expect(
        redisGroup.queryByRole("button", { name: "Clear saved value" }),
      ).not.toBeInTheDocument();
    });
  });
});
