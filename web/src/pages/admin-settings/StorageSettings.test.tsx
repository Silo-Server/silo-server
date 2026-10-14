import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import StorageSettings from "./StorageSettings";

window.HTMLElement.prototype.hasPointerCapture ??= () => false;
window.HTMLElement.prototype.setPointerCapture ??= () => {};
window.HTMLElement.prototype.releasePointerCapture ??= () => {};
window.HTMLElement.prototype.scrollIntoView ??= () => {};

const settingsFormMock = vi.fn();
const useCheckAdminSettingsConnectionMock = vi.fn();
const createStorageTransitionMock = vi.fn();
const cancelStorageTransitionMock = vi.fn();
const storageTransitionCapabilitiesMock = vi.fn();
const sourceHealthMock = vi.fn();
const taskJobsMock = vi.fn();

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

// Mirrors the server registry's restart prefixes: every artwork/s3 key restarts;
// metadata.cache_images hot-reloads. Tests that need a
// different registry override the mock.
const restartKeysMock = vi.fn(() => ({
  has: (key: string) => /^(artwork|s3)\./.test(key),
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
  useAdminServerStatus: () => ({
    data: serverStatus.current?.artwork_storage
      ? {
          ...serverStatus.current,
          artwork_storage: { status_known: true, ...serverStatus.current.artwork_storage },
        }
      : serverStatus.current,
    isPending: serverStatus.isPending,
    isError: serverStatus.isError,
    refetch: serverStatus.refetch,
  }),
  useCreateStorageTransition: () => ({
    mutateAsync: createStorageTransitionMock,
    isPending: false,
  }),
  useStorageTransitionCapabilities: () => storageTransitionCapabilitiesMock(),
  useStorageTransitionSourceHealth: (...args: unknown[]) => sourceHealthMock(...args),
  useCancelStorageTransition: () => ({
    mutateAsync: cancelStorageTransitionMock,
    isPending: false,
  }),
}));

vi.mock("@/hooks/queries/admin/taskJobs", () => ({
  useAdminTaskJobs: () => taskJobsMock(),
}));

const serverStatus: {
  current:
    | {
        artwork_storage?: {
          backend?: string;
          locked: boolean;
          private_locked?: boolean;
          status_known?: boolean;
        };
      }
    | undefined;
  isPending: boolean;
  isError: boolean;
  refetch: ReturnType<typeof vi.fn>;
} = {
  current: { artwork_storage: { backend: "local", locked: false } },
  isPending: false,
  isError: false,
  refetch: vi.fn(),
};

useCheckAdminSettingsConnectionMock.mockReturnValue({ isPending: false, mutateAsync: vi.fn() });
storageTransitionCapabilitiesMock.mockReturnValue({
  data: { state: "available", allowed: true },
  isPending: false,
  isError: false,
});
function mockUncheckedSourceHealth() {
  sourceHealthMock.mockReturnValue({
    data: undefined,
    isPending: false,
    isFetching: false,
    isError: false,
    refetch: vi.fn(),
  });
}
mockUncheckedSourceHealth();
taskJobsMock.mockReturnValue({ data: [], isFetching: false, refetch: vi.fn() });

type FormOverrides = Partial<Record<string, unknown>>;

function mockForm(overrides: FormOverrides = {}) {
  const form = {
    isLoading: false,
    getValue: (key: string) => (key === "s3.public_url_auth" ? "presigned" : ""),
    getPersistedValue: () => "",
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

describe("StorageSettings", () => {
  afterEach(() => {
    // Disclosures remember being opened; start every case collapsed.
    localStorage.clear();
    serverStatus.current = { artwork_storage: { backend: "local", locked: false } };
    serverStatus.isPending = false;
    serverStatus.isError = false;
    serverStatus.refetch.mockReset();
    sourceHealthMock.mockReset();
    mockUncheckedSourceHealth();
    taskJobsMock.mockReset();
    taskJobsMock.mockReturnValue({ data: [], isFetching: false, refetch: vi.fn() });
    useCheckAdminSettingsConnectionMock.mockReset();
    useCheckAdminSettingsConnectionMock.mockReturnValue({
      isPending: false,
      mutateAsync: vi.fn(),
    });
    storageTransitionCapabilitiesMock.mockReset();
    storageTransitionCapabilitiesMock.mockReturnValue({
      data: { state: "available", allowed: true },
      isPending: false,
      isError: false,
    });
  });

  it.each([
    ["loading", true, false],
    ["failed", false, true],
  ])("holds infrastructure editing while server lock status is %s", (state, isPending, isError) => {
    serverStatus.current = undefined;
    serverStatus.isPending = isPending;
    serverStatus.isError = isError;
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.public_bucket"],
      isDirty: (key: string) => key === "s3.public_bucket",
      getValue: (key: string) =>
        key === "s3.public_bucket"
          ? "new-artwork"
          : key === "s3.public_url_auth"
            ? "presigned"
            : "",
    });

    render(<StorageSettings />);

    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Bucket")).not.toBeInTheDocument();
    expect(
      state === "failed"
        ? screen.getByRole("alert")
        : screen.getByRole("status", { name: "Loading settings" }),
    ).toBeInTheDocument();
  });

  it("keeps the backend editable until artwork has been stored", () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: false } };
    mockForm();
    render(<StorageSettings />);
    expect(screen.getByRole("combobox", { name: "Backend" })).toBeEnabled();
    expect(screen.queryByText(/Locked to/)).not.toBeInTheDocument();
    expect(screen.getByLabelText("Local storage path")).toBeEnabled();
  });

  it("opens the managed transition when a locked S3 backend changes", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    mockForm({ getValue: (key: string) => (key === "artwork.storage_backend" ? "auto" : "") });
    render(<StorageSettings />);
    const backend = screen.getByRole("combobox", { name: "Backend" });
    expect(backend).toBeEnabled();
    expect(backend).toHaveTextContent("S3");
    expect(screen.queryByText(/Locked to S3/)).not.toBeInTheDocument();
    expect(screen.getByText(/Choose Automatic or Local disk/)).toBeInTheDocument();
    expect(screen.getByLabelText("Local storage path")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Change storage" })).not.toBeInTheDocument();

    await userEvent.click(backend);
    await userEvent.click(screen.getByRole("option", { name: "Automatic" }));
    expect(screen.getByRole("dialog", { name: "Change artwork storage" })).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Target" })).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Target" })).toHaveTextContent(
      "Local disk (default Silo behavior)",
    );
    expect(screen.queryByText("Configured S3 location")).not.toBeInTheDocument();
    serverStatus.current = undefined;
  });

  it("does not open a managed transition when capability discovery is unavailable", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    storageTransitionCapabilitiesMock.mockReturnValue({
      data: { state: "unsupported", allowed: false },
      isPending: false,
      isError: false,
    });
    const form = mockForm({
      getValue: (key: string) => (key === "artwork.storage_backend" ? "s3" : ""),
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
    await userEvent.click(screen.getByRole("option", { name: "Local disk" }));

    expect(
      screen.queryByRole("dialog", { name: "Change artwork storage" }),
    ).not.toBeInTheDocument();
    expect(form.setValue).not.toHaveBeenCalledWith("artwork.storage_backend", "local");
  });

  it("queues an explicit start-fresh transition back to local storage", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    const form = mockForm({
      getValue: (key: string) =>
        key === "artwork.storage_backend"
          ? "s3"
          : key === "artwork.local_path"
            ? "/srv/silo/artwork"
            : "",
    });
    createStorageTransitionMock.mockResolvedValueOnce({ job: { id: "transition-1" } });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
    await userEvent.click(screen.getByRole("option", { name: "Local disk" }));

    expect(sourceHealthMock).toHaveBeenCalledWith(true, true);
    expect(
      screen.getByText("Backfill Metadata Images downloads from saved provider URLs, not old S3."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: /Start fresh/ }));
    await userEvent.click(screen.getByRole("button", { name: "Queue transition" }));

    await waitFor(() =>
      expect(createStorageTransitionMock).toHaveBeenCalledWith({
        policy: "start_fresh",
        values: {
          "artwork.storage_backend": "local",
          "artwork.local_path": "/srv/silo/artwork",
        },
      }),
    );
    expect(form.resetValue).toHaveBeenCalledWith("artwork.storage_backend");
    expect(form.resetValue).toHaveBeenCalledWith("artwork.local_path");
    expect(form.discard).not.toHaveBeenCalled();
    serverStatus.current = undefined;
  });

  it("disables copy policies when the current S3 source is unreachable", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    const refetch = vi.fn();
    sourceHealthMock.mockReturnValue({
      data: {
        current_backend: "s3",
        reachable: false,
        public_configured: true,
        public_reachable: false,
        private_configured: true,
        private_reachable: false,
        message: "Current public and private S3 storage are unreachable.",
      },
      isPending: false,
      isFetching: false,
      isError: false,
      refetch,
    });
    mockForm({
      getValue: (key: string) =>
        key === "artwork.storage_backend"
          ? "s3"
          : key === "artwork.local_path"
            ? "/srv/silo/artwork"
            : "",
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
    await userEvent.click(screen.getByRole("option", { name: "Local disk" }));

    expect(
      within(screen.getByRole("alert")).getByText(
        /Current public and private S3 storage are unreachable/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Preserve personal uploads/ })).toBeDisabled();
    expect(screen.getByRole("radio", { name: /Migrate everything/ })).toBeDisabled();
    expect(screen.getByRole("radio", { name: /Start fresh/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Queue transition" })).toBeDisabled();

    await userEvent.click(screen.getByRole("radio", { name: /Start fresh/ }));
    expect(screen.getByRole("button", { name: "Queue transition" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Retry check" }));
    expect(refetch).toHaveBeenCalledOnce();
    serverStatus.current = undefined;
  });

  it("manages the storage keys and provider artwork caching in one form", () => {
    mockForm();

    renderToStaticMarkup(<StorageSettings />);

    const calls = settingsFormMock.mock.calls as [{ keys: string[] }][];
    const keys = calls[calls.length - 1]?.[0].keys ?? [];
    expect(keys).toEqual(
      expect.arrayContaining(["s3.public_bucket", "s3.private_bucket", "metadata.cache_images"]),
    );
    // Redis and the Postgres pool moved to General, which saves those keys itself.
    expect(keys.filter((key) => /^(redis|database)\./.test(key))).toEqual([]);
    // Log retention moved to the Logging page, which saves those keys itself.
    expect(keys.filter((key) => /^(opslog|policy)\./.test(key))).toEqual([]);
    // The disabled Litestream storage tab is gone; its keys keep working through the API.
    expect(keys.filter((key) => key.startsWith("s3.user_db_"))).toEqual([]);
    // The per-user database backend and its SQLite tuning are Hidden: one usable
    // option and two fields that could never show. The API still serves them.
    expect(keys.filter((key) => key.startsWith("userdb."))).toEqual([]);
  });

  it("shows provider artwork caching in the Storage group and stages edits", async () => {
    const form = mockForm({
      getValue: (key: string) =>
        key === "metadata.cache_images" ? "true" : key === "s3.public_url_auth" ? "presigned" : "",
    });
    render(<StorageSettings />);

    const storage = within(screen.getByRole("group", { name: "Storage" }));
    const toggle = storage.getByRole("switch", { name: "Save provider artwork to storage" });
    expect(toggle).toBeChecked();
    // It hot-reloads, so the group no longer claims a restart for every row.
    expect(storage.queryByText("Changes apply after a restart")).not.toBeInTheDocument();
    expect(storage.getAllByLabelText("Takes effect after a server restart")).toHaveLength(2);
    await userEvent.click(toggle);
    expect(form.setValue).toHaveBeenCalledWith("metadata.cache_images", "false");
  });

  it("saves provider artwork caching directly on a locked install", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const form = mockForm({
      dirtyCount: 1,
      dirtyKeys: ["metadata.cache_images"],
      isDirty: (key: string) => key === "metadata.cache_images",
      getValue: (key: string) =>
        key === "artwork.storage_backend"
          ? "local"
          : key === "metadata.cache_images"
            ? "false"
            : key === "s3.public_url_auth"
              ? "presigned"
              : "",
      getPersistedValue: (key: string) =>
        key === "artwork.storage_backend" ? "local" : key === "metadata.cache_images" ? "true" : "",
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).toHaveBeenCalledWith(undefined);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    serverStatus.current = undefined;
  });

  it.each([
    ["Automatic without a bucket", "", ""],
    ["Automatic without a bucket, saved as auto", "auto", ""],
    ["S3", "s3", ""],
    ["Automatic with a saved bucket", "auto", "artwork"],
  ])("shows public and private storage for %s", (_, backend, savedBucket) => {
    mockForm({
      getPersistedValue: (key: string) =>
        key === "artwork.storage_backend" ? backend : key === "s3.public_bucket" ? savedBucket : "",
      getValue: (key: string) =>
        key === "artwork.storage_backend"
          ? backend
          : key === "s3.public_bucket"
            ? savedBucket
            : key === "s3.public_url_auth"
              ? "presigned"
              : "",
    });
    render(<StorageSettings />);

    const publicStorage = within(screen.getByRole("group", { name: "Public storage" }));
    expect(publicStorage.getByLabelText("Bucket")).toBeVisible();
    const privateStorage = within(screen.getByRole("group", { name: "Private storage" }));
    expect(privateStorage.getByLabelText("Bucket")).toBeVisible();
    expect(screen.queryByRole("group", { name: "S3 storage" })).not.toBeInTheDocument();
  });

  // Public storage serves only S3 and Automatic, and local disk keeps private
  // files in the local path, so Local disk shows neither bucket section.
  it.each([
    ["Local disk", ""],
    ["Local disk with a leftover public bucket", "artwork"],
  ])("hides both bucket sections for %s", (_, savedBucket) => {
    mockForm({
      getPersistedValue: (key: string) =>
        key === "artwork.storage_backend" ? "local" : key === "s3.public_bucket" ? savedBucket : "",
      getValue: (key: string) =>
        key === "artwork.storage_backend"
          ? "local"
          : key === "s3.public_bucket"
            ? savedBucket
            : key === "s3.public_url_auth"
              ? "presigned"
              : "",
    });
    render(<StorageSettings />);

    expect(screen.queryByRole("group", { name: "Public storage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Private storage" })).not.toBeInTheDocument();
  });

  // A saved private bucket holds the avatars even on local disk, so it stays
  // in view where it can be changed or cleared.
  it("keeps a saved private bucket in view on Local disk", () => {
    mockForm({
      getPersistedValue: (key: string) =>
        key === "artwork.storage_backend" ? "local" : key === "s3.private_bucket" ? "ops" : "",
      getValue: (key: string) =>
        key === "artwork.storage_backend" ? "local" : key === "s3.private_bucket" ? "ops" : "",
    });
    render(<StorageSettings />);

    const privateStorage = within(screen.getByRole("group", { name: "Private storage" }));
    expect(privateStorage.getByLabelText("Bucket")).toHaveValue("ops");
  });

  it("shows a staged public endpoint after the backend goes back to Local disk", () => {
    mockForm({
      dirtyCount: 2,
      isDirty: (key: string) => key === "artwork.storage_backend" || key === "s3.public_endpoint",
      getValue: (key: string) =>
        key === "artwork.storage_backend"
          ? "local"
          : key === "s3.public_endpoint"
            ? "https://s3.example"
            : key === "s3.public_url_auth"
              ? "presigned"
              : "",
    });
    render(<StorageSettings />);

    expect(
      within(screen.getByRole("group", { name: "Public storage" })).getByLabelText("Endpoint"),
    ).toHaveValue("https://s3.example");
  });

  // Choosing S3 stages the backend instead of opening the review, so Public
  // storage appears and its bucket can be filled in before Save.
  it("stages S3 on a locked local install instead of opening the review", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const form = mockForm({
      getPersistedValue: (key: string) => (key === "artwork.storage_backend" ? "local" : ""),
      getValue: (key: string) =>
        key === "artwork.storage_backend"
          ? "local"
          : key === "s3.public_url_auth"
            ? "presigned"
            : "",
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
    await userEvent.click(screen.getByRole("option", { name: "S3" }));

    expect(form.setValue).toHaveBeenCalledWith("artwork.storage_backend", "s3");
    expect(
      screen.queryByRole("dialog", { name: "Change artwork storage" }),
    ).not.toBeInTheDocument();
    serverStatus.current = undefined;
  });

  it("shows every private storage field without a disclosure", () => {
    mockForm();
    render(<StorageSettings />);

    const privateStorage = within(screen.getByRole("group", { name: "Private storage" }));
    expect(
      privateStorage.getByText(/on local disk they stay in the local storage path/),
    ).toBeInTheDocument();
    for (const label of [
      "Endpoint",
      "Bucket",
      "Access Key",
      "Secret Key",
      "Region",
      "Put the bucket name in the URL path",
      "Folder inside the bucket",
    ]) {
      expect(privateStorage.getByLabelText(label)).toBeVisible();
    }
    expect(privateStorage.getByRole("button", { name: "Check Connection" })).toBeVisible();
    expect(privateStorage.queryByRole("button", { name: /Advanced/ })).not.toBeInTheDocument();
  });

  it("shows the public bucket connection and link mode with no disclosure for signed links", () => {
    mockForm();
    render(<StorageSettings />);

    const publicStorage = within(screen.getByRole("group", { name: "Public storage" }));
    for (const label of [
      "Endpoint",
      "Bucket",
      "Region",
      "Access Key",
      "Secret Key",
      "Put the bucket name in the URL path",
      "Folder inside the bucket",
      "How asset links are authorized",
    ]) {
      expect(publicStorage.getByLabelText(label)).toBeVisible();
    }
    expect(publicStorage.getByRole("button", { name: "Check Connection" })).toBeVisible();
    // Signed links need neither a CDN address nor a token.
    expect(publicStorage.queryByRole("button", { name: /Advanced/ })).not.toBeInTheDocument();
    expect(publicStorage.queryByLabelText("Address clients download from")).not.toBeInTheDocument();
  });

  it.each([
    ["public", 1, ["Address clients download from"]],
    [
      "cloudflare_token",
      4,
      ["Address clients download from", "Token Secret", "Token query parameter", "Link lifetime"],
    ],
  ])(
    "keeps the %s link settings behind public storage's Advanced disclosure",
    async (urlAuth, count, labels) => {
      mockForm({
        getValue: (key: string) => (key === "s3.public_url_auth" ? urlAuth : ""),
      });
      render(<StorageSettings />);

      const publicStorage = within(screen.getByRole("group", { name: "Public storage" }));
      for (const label of labels) {
        expect(publicStorage.queryByLabelText(label)).not.toBeInTheDocument();
      }
      await userEvent.click(
        publicStorage.getByRole("button", {
          name: `Advanced · ${count} setting${count === 1 ? "" : "s"}`,
        }),
      );
      for (const label of labels) {
        expect(publicStorage.getByLabelText(label)).toBeVisible();
      }
    },
  );

  it("opens public storage's Advanced disclosure while one of its fields is unsaved", () => {
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.public_read_endpoint"],
      isDirty: (key: string) => key === "s3.public_read_endpoint",
      getValue: (key: string) =>
        key === "s3.public_url_auth"
          ? "public"
          : key === "s3.public_read_endpoint"
            ? "https://cdn.example"
            : "",
    });
    render(<StorageSettings />);

    expect(
      within(screen.getByRole("group", { name: "Public storage" })).getByLabelText(
        "Address clients download from",
      ),
    ).toHaveValue("https://cdn.example");
  });

  it("warns about a staged private bucket", () => {
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.private_bucket"],
      isDirty: (key: string) => key === "s3.private_bucket",
      getValue: (key: string) => (key === "s3.private_bucket" ? "private-new" : ""),
    });
    render(<StorageSettings />);

    const privateStorage = within(screen.getByRole("group", { name: "Private storage" }));
    expect(privateStorage.getByLabelText("Bucket")).toHaveValue("private-new");
    expect(privateStorage.getByText("Storage location change")).toBeVisible();
  });

  it("saves unrelated edits before opening an S3 location transition", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    const save = vi.fn().mockResolvedValue(undefined);
    mockForm({
      dirtyCount: 2,
      dirtyKeys: ["s3.public_bucket", "server.log_level"],
      isDirty: (key: string) => key === "s3.public_bucket" || key === "server.log_level",
      save,
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_bucket") return "new-artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    expect(save).toHaveBeenCalledWith(["server.log_level"]);
    expect(screen.getByRole("dialog", { name: "Change artwork storage" })).toBeVisible();
  });

  it.each([
    [
      "unsupported",
      { data: { state: "unsupported", allowed: false }, isPending: false, isError: false },
    ],
    ["loading", { data: undefined, isPending: true, isError: false }],
    ["failed", { data: undefined, isPending: false, isError: true }],
  ])(
    "keeps staged location and unrelated edits when transition discovery is %s",
    async (_, capabilities) => {
      serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
      storageTransitionCapabilitiesMock.mockReturnValue(capabilities);
      const form = mockForm({
        dirtyCount: 2,
        dirtyKeys: ["s3.public_bucket", "server.log_level"],
        isDirty: (key: string) => key === "s3.public_bucket" || key === "server.log_level",
        getValue: (key: string) => {
          if (key === "artwork.storage_backend") return "s3";
          if (key === "s3.public_bucket") return "new-artwork";
          if (key === "s3.public_url_auth") return "presigned";
          return "";
        },
      });
      render(<StorageSettings />);

      await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

      expect(form.save).not.toHaveBeenCalled();
      expect(
        screen.queryByRole("dialog", { name: "Change artwork storage" }),
      ).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Review transition" })).toBeVisible();
    },
  );

  it("opens an S3-to-S3 transition when a locked endpoint or bucket is saved", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    const form = mockForm({
      dirtyCount: 2,
      isDirty: (key: string) => key === "s3.public_endpoint" || key === "s3.public_bucket",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_endpoint") return "https://new-s3.example";
        if (key === "s3.public_bucket") return "new-artwork";
        if (key === "s3.private_endpoint") return "https://private.example";
        if (key === "s3.private_bucket") return "private-artifacts";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    createStorageTransitionMock.mockResolvedValueOnce({ job: { id: "transition-s3" } });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change artwork storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "New S3 location",
    );
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "new-artwork · https://new-s3.example",
    );
    await userEvent.click(within(dialog).getByRole("button", { name: "Queue transition" }));

    await waitFor(() =>
      expect(createStorageTransitionMock).toHaveBeenCalledWith({
        policy: "preserve_uploads",
        values: {
          "artwork.storage_backend": "s3",
          "s3.public_endpoint": "https://new-s3.example",
          "s3.public_bucket": "new-artwork",
        },
      }),
    );
    expect(form.resetValue).toHaveBeenCalledWith("s3.public_endpoint");
    expect(form.resetValue).toHaveBeenCalledWith("s3.public_bucket");
    serverStatus.current = undefined;
  });

  it("routes a locked local path change through a local-to-local transition", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const form = mockForm({
      dirtyCount: 1,
      dirtyKeys: ["artwork.local_path"],
      isDirty: (key: string) => key === "artwork.local_path",
      getPersistedValue: (key: string) =>
        key === "artwork.local_path" ? "/srv/silo/artwork-old" : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "artwork.local_path") return "/srv/silo/artwork-new";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    createStorageTransitionMock.mockResolvedValueOnce({ job: { id: "transition-local-path" } });
    render(<StorageSettings />);

    expect(screen.getByLabelText("Local storage path")).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change artwork storage" });
    expect(within(dialog).getByLabelText("Local artwork path")).toHaveValue(
      "/srv/silo/artwork-new",
    );
    await userEvent.click(within(dialog).getByRole("button", { name: "Queue transition" }));
    await waitFor(() =>
      expect(createStorageTransitionMock).toHaveBeenCalledWith({
        policy: "preserve_uploads",
        values: {
          "artwork.storage_backend": "local",
          "artwork.local_path": "/srv/silo/artwork-new",
        },
      }),
    );
    expect(form.save).not.toHaveBeenCalled();
  });

  it("describes private data when a local root moves without a private bucket", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["artwork.local_path"],
      isDirty: (key: string) => key === "artwork.local_path",
      getPersistedValue: (key: string) =>
        key === "artwork.local_path" ? "/srv/silo/artwork-old" : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "artwork.local_path") return "/srv/silo/artwork-new";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change artwork storage" });
    expect(
      within(dialog).getByText(/copies branding, collection and library posters, profile avatars/i),
    ).toBeVisible();
    expect(
      within(dialog).getByText(
        /Preserve personal uploads and Start fresh leave them in the old location/i,
      ),
    ).toBeVisible();
    expect(
      within(dialog)
        .getByRole("radio", { name: /Start fresh/i })
        .closest("label"),
    ).toHaveTextContent(
      /Profile avatars also remain in the old storage and are unavailable after the switch/i,
    );
    expect(
      within(dialog).queryByText(
        /Profile avatars, diagnostic bundles, and catalog job artifacts stay in their current storage/,
      ),
    ).not.toBeInTheDocument();
  });

  it.each(["/srv/silo/artwork/", "/srv//silo/./artwork/"])(
    "does not start a transition for a restyled local path %s",
    (path) => {
      serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
      mockForm({
        dirtyCount: 1,
        dirtyKeys: ["artwork.local_path"],
        isDirty: (key: string) => key === "artwork.local_path",
        getPersistedValue: (key: string) =>
          key === "artwork.local_path" ? "/srv/silo/artwork" : "",
        getValue: (key: string) =>
          key === "artwork.local_path" ? path : key === "artwork.storage_backend" ? "local" : "",
      });
      render(<StorageSettings />);

      expect(screen.getByRole("button", { name: "Save" })).toBeVisible();
      expect(screen.queryByRole("button", { name: "Review transition" })).not.toBeInTheDocument();
    },
  );

  it.each([
    ["failed refetch", true, true],
    ["unknown lock state", false, false],
  ])("keeps other settings usable during %s", async (_, isError, statusKnown) => {
    createStorageTransitionMock.mockClear();
    serverStatus.current = {
      artwork_storage: { backend: "local", locked: true, status_known: statusKnown },
    };
    serverStatus.isError = isError;
    const form = mockForm({
      dirtyCount: 2,
      dirtyKeys: ["artwork.local_path", "metadata.cache_images"],
      isDirty: (key: string) => key === "artwork.local_path" || key === "metadata.cache_images",
      getPersistedValue: (key: string) =>
        key === "artwork.local_path" ? "/srv/silo/artwork-old" : "",
      getValue: (key: string) => (key === "artwork.local_path" ? "/srv/silo/artwork-new" : ""),
    });
    render(<StorageSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent("Storage lock status is unavailable");
    expect(screen.getByRole("switch", { name: "Save provider artwork to storage" })).toBeEnabled();
    expect(screen.getByRole("combobox", { name: "Backend" })).toBeDisabled();
    expect(screen.getByLabelText("Local storage path")).toBeDisabled();
    for (const name of ["Public storage", "Private storage"]) {
      expect(screen.queryByRole("group", { name })).not.toBeInTheDocument();
    }
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).toHaveBeenCalledWith(["metadata.cache_images"]);
    expect(createStorageTransitionMock).not.toHaveBeenCalled();
  });

  it("holds the public bucket location while lock status is unknown", () => {
    serverStatus.current = {
      artwork_storage: { backend: "s3", locked: true, status_known: false },
    };
    mockForm({
      getPersistedValue: (key: string) => (key === "artwork.storage_backend" ? "s3" : ""),
      getValue: (key: string) =>
        key === "artwork.storage_backend" ? "s3" : key === "s3.public_url_auth" ? "presigned" : "",
    });
    render(<StorageSettings />);

    const publicStorage = within(screen.getByRole("group", { name: "Public storage" }));
    expect(publicStorage.getByLabelText("Endpoint")).toBeDisabled();
    expect(publicStorage.getByLabelText("Bucket")).toBeDisabled();
    // Credentials are not a location, so they stay editable.
    expect(publicStorage.getByLabelText("Access Key")).toBeEnabled();
  });

  it("holds a staged storage location until lock status can be retried", async () => {
    serverStatus.current = {
      artwork_storage: { backend: "local", locked: false, status_known: false },
    };
    const form = mockForm({
      dirtyCount: 1,
      dirtyKeys: ["artwork.local_path"],
      isDirty: (key: string) => key === "artwork.local_path",
      getValue: (key: string) => (key === "artwork.local_path" ? "/srv/silo/artwork-new" : ""),
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Retry status" }));
    expect(serverStatus.refetch).toHaveBeenCalledOnce();
  });

  // After a committed local-to-S3 transition, settings already name S3 while
  // this process still runs on local disk until it restarts.
  it("saves storage credentials directly while a committed switch awaits restart", async () => {
    createStorageTransitionMock.mockClear();
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    taskJobsMock.mockReturnValue({
      data: [
        {
          id: "committed",
          status: "completed",
          message: "",
          result_payload: { phase: "restart_pending", manual_restart_required: true },
        },
      ],
      isFetching: false,
      refetch: vi.fn(),
    });
    const saved: Record<string, string> = {
      "artwork.storage_backend": "s3",
      "s3.public_endpoint": "https://s3.example",
      "s3.public_bucket": "new-artwork",
    };
    const form = mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.public_access_key"],
      isDirty: (key: string) => key === "s3.public_access_key",
      getPersistedValue: (key: string) => saved[key] ?? "",
      getValue: (key: string) => (key === "s3.public_access_key" ? "NEWKEY" : (saved[key] ?? "")),
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).toHaveBeenCalled();
    expect(createStorageTransitionMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  // Saved directly while storage was still unlocked; the first local write then
  // recorded local disk. Without a committed transition the lock compares with
  // the running backend, so only a transition can move storage now.
  it("routes storage edits to a transition when saved settings differ without a commit", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const saved: Record<string, string> = {
      "artwork.storage_backend": "s3",
      "s3.public_endpoint": "https://s3.example",
      "s3.public_bucket": "new-artwork",
    };
    const form = mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.public_access_key"],
      isDirty: (key: string) => key === "s3.public_access_key",
      getPersistedValue: (key: string) => saved[key] ?? "",
      getValue: (key: string) => (key === "s3.public_access_key" ? "NEWKEY" : (saved[key] ?? "")),
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));
    expect(form.save).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it("holds a new bucket's credentials with it while lock status is unknown", async () => {
    serverStatus.current = {
      artwork_storage: { backend: "s3", locked: true, status_known: false },
    };
    const edited = [
      "s3.public_bucket",
      "s3.public_access_key",
      "s3.public_secret_key",
      "metadata.cache_images",
    ];
    const form = mockForm({
      dirtyCount: edited.length,
      dirtyKeys: edited,
      isDirty: (key: string) => edited.includes(key),
      getPersistedValue: (key: string) => (key === "s3.public_bucket" ? "artwork-old" : ""),
      getValue: (key: string) => (key === "s3.public_bucket" ? "artwork-new" : ""),
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(form.save).toHaveBeenCalledWith(["metadata.cache_images"]);
  });

  it("routes an Automatic local backend to S3 when a public bucket is added", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const form = mockForm({
      dirtyCount: 2,
      dirtyKeys: ["s3.public_endpoint", "s3.public_bucket"],
      isDirty: (key: string) => key === "s3.public_endpoint" || key === "s3.public_bucket",
      getPersistedValue: (key: string) => (key === "artwork.storage_backend" ? "auto" : ""),
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "auto";
        if (key === "s3.public_endpoint") return "https://s3.example";
        if (key === "s3.public_bucket") return "artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    createStorageTransitionMock.mockResolvedValueOnce({ job: { id: "transition-auto-s3" } });
    render(<StorageSettings />);

    expect(
      screen.getByText(/Saving this bucket opens a managed transition from local storage/),
    ).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change artwork storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "New S3 location",
    );
    await userEvent.click(within(dialog).getByRole("radio", { name: /Start fresh/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Queue transition" }));
    await waitFor(() =>
      expect(createStorageTransitionMock).toHaveBeenCalledWith({
        policy: "start_fresh",
        values: {
          "artwork.storage_backend": "s3",
          "s3.public_endpoint": "https://s3.example",
          "s3.public_bucket": "artwork",
        },
      }),
    );
    expect(form.save).not.toHaveBeenCalled();
  });

  it("routes Local to Automatic with a saved public bucket through an S3 transition", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const form = mockForm({
      getPersistedValue: (key: string) =>
        key === "artwork.storage_backend" ? "local" : key === "s3.public_bucket" ? "artwork" : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "s3.public_bucket") return "artwork";
        if (key === "s3.public_endpoint") return "https://s3.example";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
    await userEvent.click(screen.getByRole("option", { name: "Automatic" }));

    expect(form.setValue).toHaveBeenCalledWith("artwork.storage_backend", "auto");
    expect(
      screen.queryByRole("dialog", { name: "Change artwork storage" }),
    ).not.toBeInTheDocument();
  });

  it("reviews a staged Local to Automatic switch with a saved public bucket as an S3 move", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["artwork.storage_backend"],
      isDirty: (key: string) => key === "artwork.storage_backend",
      getPersistedValue: (key: string) =>
        key === "artwork.storage_backend" ? "local" : key === "s3.public_bucket" ? "artwork" : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "auto";
        if (key === "s3.public_bucket") return "artwork";
        if (key === "s3.public_endpoint") return "https://s3.example";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change artwork storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "New S3 location",
    );
  });

  it("routes Automatic S3 to local when its public bucket is cleared", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    const form = mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.public_bucket"],
      isDirty: (key: string) => key === "s3.public_bucket",
      getPersistedValue: (key: string) =>
        key === "artwork.storage_backend" ? "auto" : key === "s3.public_bucket" ? "artwork" : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "auto";
        if (key === "artwork.local_path") return "/srv/silo/artwork";
        if (key === "s3.public_bucket") return "";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    createStorageTransitionMock.mockResolvedValueOnce({ job: { id: "transition-auto-local" } });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));
    const dialog = screen.getByRole("dialog", { name: "Change artwork storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent("Local disk");
    await userEvent.click(within(dialog).getByRole("radio", { name: /Start fresh/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Queue transition" }));

    await waitFor(() =>
      expect(createStorageTransitionMock).toHaveBeenCalledWith({
        policy: "start_fresh",
        values: {
          "artwork.storage_backend": "local",
          "artwork.local_path": "/srv/silo/artwork",
        },
      }),
    );
    expect(form.save).not.toHaveBeenCalled();
    expect(form.resetValue).toHaveBeenCalledWith("s3.public_bucket");
  });

  it("saves S3 to Automatic directly when the public bucket remains configured", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    const form = mockForm({
      getValue: (key: string) =>
        key === "artwork.storage_backend" ? "s3" : key === "s3.public_bucket" ? "artwork" : "",
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
    await userEvent.click(screen.getByRole("option", { name: "Automatic" }));

    expect(
      screen.queryByRole("dialog", { name: "Change artwork storage" }),
    ).not.toBeInTheDocument();
    expect(form.setValue).toHaveBeenCalledWith("artwork.storage_backend", "auto");
  });

  it("holds storage location fields while unrelated settings save before transition review", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    let finishSave: (() => void) | undefined;
    const save = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishSave = resolve;
        }),
    );
    mockForm({
      dirtyCount: 2,
      dirtyKeys: ["artwork.local_path", "metadata.cache_images"],
      isDirty: (key: string) => key === "artwork.local_path" || key === "metadata.cache_images",
      // A saved private bucket keeps Private storage on the Local disk page.
      getPersistedValue: (key: string) =>
        key === "artwork.local_path"
          ? "/srv/silo/artwork-old"
          : key === "s3.private_bucket"
            ? "ops"
            : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "artwork.local_path") return "/srv/silo/artwork-new";
        if (key === "s3.private_bucket") return "ops";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
      save,
    });
    render(<StorageSettings />);
    const privateStorage = within(screen.getByRole("group", { name: "Private storage" }));
    expect(privateStorage.getByLabelText("Endpoint")).toBeEnabled();

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));
    expect(save).toHaveBeenCalledWith(["metadata.cache_images"]);
    expect(screen.getByLabelText("Local storage path")).toBeDisabled();
    expect(privateStorage.getByLabelText("Endpoint")).toBeDisabled();
    expect(privateStorage.getByLabelText("Bucket")).toBeDisabled();

    finishSave?.();
    expect(await screen.findByRole("dialog", { name: "Change artwork storage" })).toBeVisible();
    expect(screen.getByLabelText("Local artwork path")).toHaveValue("/srv/silo/artwork-new");
  });

  it("keeps public bucket edits as settings on an explicit Local backend", () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.public_bucket"],
      isDirty: (key: string) => key === "s3.public_bucket",
      getPersistedValue: (key: string) => (key === "artwork.storage_backend" ? "local" : ""),
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "s3.public_bucket") return "artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    expect(screen.getByRole("button", { name: "Save" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Review transition" })).not.toBeInTheDocument();
  });

  it.each([
    ["an explicit default port", "https://s3.example:443"],
    ["a trailing slash", "https://s3.example/"],
  ])("routes %s in a locked S3 endpoint through a transition", async (_, endpoint) => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.public_endpoint"],
      isDirty: (key: string) => key === "s3.public_endpoint",
      getPersistedValue: (key: string) =>
        key === "s3.public_endpoint" ? "https://s3.example" : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_endpoint") return endpoint;
        if (key === "s3.public_bucket") return "artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));
    expect(screen.getByRole("dialog", { name: "Change artwork storage" })).toBeVisible();
  });

  it("routes an explicit port in a locked private endpoint through a private transition", async () => {
    serverStatus.current = {
      artwork_storage: { backend: "local", locked: false, private_locked: true },
    };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.private_endpoint"],
      isDirty: (key: string) => key === "s3.private_endpoint",
      getPersistedValue: (key: string) => {
        if (key === "s3.private_endpoint") return "https://private.example";
        if (key === "s3.private_bucket") return "private";
        return "";
      },
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "s3.private_endpoint") return "https://private.example:443";
        if (key === "s3.private_bucket") return "private";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));
    expect(screen.getByRole("dialog", { name: "Change private storage" })).toBeVisible();
  });

  it("warns that Start fresh leaves downloaded subtitles behind on local-to-S3 moves", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["artwork.storage_backend"],
      isDirty: (key: string) => key === "artwork.storage_backend",
      getPersistedValue: (key: string) => (key === "artwork.storage_backend" ? "local" : ""),
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_bucket") return "artwork";
        if (key === "s3.public_endpoint") return "https://s3.example";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const startFresh = screen.getByRole("radio", { name: /Start fresh/ }).closest("label");
    expect(startFresh).toHaveTextContent(/Downloaded subtitles.*unavailable after the switch/);
  });

  it("shows the kept endpoint during a bucket-only change", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    mockForm({
      dirtyCount: 1,
      isDirty: (key: string) => key === "s3.public_bucket",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_endpoint") return "https://s3.example";
        if (key === "s3.public_bucket") return "new-artwork";
        if (key === "s3.private_endpoint") return "https://private.example";
        if (key === "s3.private_bucket") return "private-artifacts";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    expect(screen.getByRole("group", { name: "Target" })).toHaveTextContent(
      "new-artwork · https://s3.example",
    );
    serverStatus.current = undefined;
  });

  it("describes a private-only S3 transition without public artwork claims", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    mockForm({
      dirtyCount: 2,
      isDirty: (key: string) => key === "s3.private_endpoint" || key === "s3.private_bucket",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_bucket") return "public-artwork";
        if (key === "s3.private_endpoint") return "https://private.example";
        if (key === "s3.private_bucket") return "private-new";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change private storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "private-new · https://private.example",
    );
    expect(within(dialog).getByRole("radio", { name: /Preserve profile avatars/ })).toBeVisible();
    expect(
      within(dialog).getByText(/Preserve profile avatars and Start fresh leave/),
    ).toBeVisible();
    expect(
      within(dialog).getByText(/Existing profile avatars will appear missing after the switch/),
    ).toBeVisible();
    expect(within(dialog).queryByText(/Provider artwork is rebuilt/)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/Backfill Metadata Images/)).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/NFO\/sidecar/)).not.toBeInTheDocument();
    expect(within(dialog).getByText(/reconciled in the background after restart/)).toBeVisible();
  });

  it("describes public-only and combined S3 changes as artwork transitions", async () => {
    for (const changedKeys of [
      new Set(["s3.public_bucket"]),
      new Set(["s3.public_bucket", "s3.private_bucket"]),
    ]) {
      serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
      mockForm({
        dirtyCount: changedKeys.size,
        isDirty: (key: string) => changedKeys.has(key),
        getValue: (key: string) => {
          if (key === "artwork.storage_backend") return "s3";
          if (key === "s3.public_bucket") return "public-new";
          if (key === "s3.private_bucket") return "private-new";
          if (key === "s3.public_url_auth") return "presigned";
          return "";
        },
      });
      const view = render(<StorageSettings />);
      await userEvent.click(screen.getByRole("button", { name: "Review transition" }));
      const dialog = screen.getByRole("dialog", { name: "Change artwork storage" });
      expect(dialog).toBeVisible();
      expect(screen.getByRole("radio", { name: /Preserve personal uploads/ })).toBeVisible();
      // Avatars move only with the private location.
      expect(
        within(dialog).getByText(
          changedKeys.has("s3.private_bucket")
            ? /profile avatars, and downloaded subtitles/
            : /posters, and downloaded subtitles\. Provider/,
        ),
      ).toBeVisible();
      expect(within(dialog).getByText(/including downloaded subtitles/)).toBeVisible();
      if (changedKeys.has("s3.private_bucket")) {
        expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
          "Private bucket: private-new",
        );
      }
      view.unmount();
    }
  });

  it("requires private S3 before offering avatar-preserving copy policies", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["artwork.storage_backend"],
      isDirty: (key: string) => key === "artwork.storage_backend",
      getPersistedValue: (key: string) => (key === "artwork.storage_backend" ? "local" : ""),
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_bucket") return "public-artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    expect(screen.getByRole("radio", { name: /Preserve personal uploads/ })).toBeDisabled();
    expect(screen.getByRole("radio", { name: /Migrate everything/ })).toBeDisabled();
    expect(screen.getAllByText(/Configure a private S3 bucket/)).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Queue transition" })).toBeDisabled();
    await userEvent.click(screen.getByRole("radio", { name: /Start fresh/ }));
    expect(screen.getByRole("button", { name: "Queue transition" })).toBeEnabled();
    serverStatus.current = undefined;
  });

  it("routes a private bucket added to a locked local install through a transition", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const form = mockForm({
      dirtyCount: 2,
      dirtyKeys: ["s3.private_endpoint", "s3.private_bucket"],
      isDirty: (key: string) => key === "s3.private_endpoint" || key === "s3.private_bucket",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "artwork.local_path") return "/srv/silo/artwork";
        if (key === "s3.private_endpoint") return "https://private.example";
        if (key === "s3.private_bucket") return "private-new";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    createStorageTransitionMock.mockResolvedValueOnce({ job: { id: "transition-private" } });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change private storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "Private bucket: private-new · https://private.example",
    );
    expect(within(dialog).queryByLabelText("Local artwork path")).not.toBeInTheDocument();
    expect(within(dialog).queryByText(/Backfill Metadata Images/)).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("radio", { name: /Migrate all private data/ }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Queue transition" }));

    await waitFor(() =>
      expect(createStorageTransitionMock).toHaveBeenCalledWith({
        policy: "migrate_all",
        values: {
          "artwork.storage_backend": "local",
          "artwork.local_path": "/srv/silo/artwork",
          "s3.private_endpoint": "https://private.example",
          "s3.private_bucket": "private-new",
        },
      }),
    );
    expect(form.save).not.toHaveBeenCalled();
  });

  it("describes removing a local install's private bucket as a move to local disk", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.private_bucket"],
      isDirty: (key: string) => key === "s3.private_bucket",
      getPersistedValue: (key: string) => (key === "s3.private_bucket" ? "private-old" : ""),
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "artwork.local_path") return "/srv/silo/artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change private storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "kept on local disk",
    );
    // The old private bucket is the copy source, so its reachability is checked.
    expect(sourceHealthMock).toHaveBeenCalledWith(true, true);
  });

  it("saves a private field that was edited back to its stored value", () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      dirtyKeys: ["s3.private_bucket"],
      isDirty: (key: string) => key === "s3.private_bucket",
      getPersistedValue: (key: string) => (key === "s3.private_bucket" ? "private" : ""),
      getValue: (key: string) => (key === "s3.private_bucket" ? "private " : ""),
    });
    render(<StorageSettings />);

    expect(screen.getByRole("button", { name: "Save" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Review transition" })).not.toBeInTheDocument();
  });

  it("warns that an S3 install without a private bucket has nowhere to keep private data", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    mockForm({
      dirtyCount: 2,
      isDirty: (key: string) => key === "s3.private_endpoint" || key === "s3.private_bucket",
      getPersistedValue: (key: string) =>
        key === "s3.private_bucket"
          ? "private-old"
          : key === "s3.private_endpoint"
            ? "https://private.example"
            : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "s3.public_bucket") return "public-artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change private storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "catalog artifacts become unavailable",
    );
    expect(within(dialog).getByRole("group", { name: "Target" })).not.toHaveTextContent(
      "local disk",
    );
  });

  it("does not queue a private bucket without its endpoint", async () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      isDirty: (key: string) => key === "s3.private_bucket",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "artwork.local_path") return "/srv/silo/artwork";
        if (key === "s3.private_bucket") return "private-new";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

    const dialog = screen.getByRole("dialog", { name: "Change private storage" });
    expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent(
      "private-new · endpoint not set",
    );
    expect(within(dialog).getByText(/Enter the private storage endpoint/)).toBeVisible();
    expect(within(dialog).getByRole("button", { name: "Queue transition" })).toBeDisabled();
  });

  it("says Start fresh leaves S3 data behind when disabling S3", async () => {
    serverStatus.current = { artwork_storage: { backend: "s3", locked: true } };
    mockForm({
      getPersistedValue: (key: string) => (key === "s3.private_bucket" ? "private" : ""),
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "s3";
        if (key === "artwork.local_path") return "/srv/silo/artwork";
        if (key === "s3.public_url_auth") return "presigned";
        return "";
      },
    });
    render(<StorageSettings />);

    await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
    await userEvent.click(screen.getByRole("option", { name: "Local disk" }));
    await userEvent.click(screen.getByRole("radio", { name: /Start fresh/ }));

    expect(screen.getByText(/Start fresh copies nothing/)).toBeVisible();
    await userEvent.click(screen.getByRole("radio", { name: /Migrate everything/ }));
    expect(screen.getByText(/is copied to local disk/)).toBeVisible();
  });

  it("saves location edits that name the same store", () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    const persisted: Record<string, string> = {
      "s3.private_endpoint": "https://private.example",
      "s3.private_bucket": "private",
      "s3.private_key_prefix": "ops",
    };
    const edited: Record<string, string> = {
      "s3.private_endpoint": "https://PRIVATE.example",
      "s3.private_bucket": "Private",
      "s3.private_key_prefix": "ops/",
    };
    mockForm({
      dirtyCount: 3,
      dirtyKeys: Object.keys(edited),
      isDirty: (key: string) => key in edited,
      getPersistedValue: (key: string) => persisted[key] ?? "",
      getValue: (key: string) => edited[key] ?? "",
    });
    render(<StorageSettings />);

    expect(screen.getByRole("button", { name: "Save" })).toBeVisible();
  });

  it("saves a leftover private prefix when there is no private bucket", () => {
    serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
    mockForm({
      dirtyCount: 1,
      isDirty: (key: string) => key === "s3.private_key_prefix",
      getPersistedValue: (key: string) => (key === "s3.private_key_prefix" ? "ops" : ""),
      getValue: () => "",
    });
    render(<StorageSettings />);

    expect(screen.getByRole("button", { name: "Save" })).toBeVisible();
  });

  it("locks only the private bucket when it alone holds data", async () => {
    serverStatus.current = {
      artwork_storage: { backend: "local", locked: false, private_locked: true },
    };
    mockForm({
      dirtyCount: 1,
      isDirty: (key: string) => key === "s3.private_bucket",
      getPersistedValue: (key: string) =>
        key === "s3.private_bucket"
          ? "private-old"
          : key === "s3.private_endpoint"
            ? "https://private.example"
            : "",
      getValue: (key: string) => {
        if (key === "artwork.storage_backend") return "local";
        if (key === "artwork.local_path") return "/srv/silo/artwork";
        if (key === "s3.private_endpoint") return "https://private.example";
        if (key === "s3.private_bucket") return "private-new";
        return "";
      },
    });
    render(<StorageSettings />);

    expect(screen.getByLabelText("Local storage path")).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Review transition" }));
    expect(screen.getByRole("dialog", { name: "Change private storage" })).toBeVisible();
  });

  it("does not show a historical completed transition or flash its refresh action", () => {
    taskJobsMock.mockReturnValue({
      data: [{ id: "done", status: "completed", message: "" }],
      isFetching: true,
      refetch: vi.fn(),
    });
    mockForm();

    render(<StorageSettings />);

    expect(screen.queryByText(/Storage transition:/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Refresh" })).not.toBeInTheDocument();
  });

  it("shows a stable dismissible failed transition", async () => {
    taskJobsMock.mockReturnValue({
      data: [
        {
          id: "failed-transition",
          status: "failed",
          message: "Storage transition failed for private/object-key",
          error_message: "PUT https://private.example.invalid/private/object-key failed",
          result_payload: {
            phase: "failed",
            verified_objects: 2,
            failure_category: "target_check_failed",
          },
        },
      ],
      isFetching: true,
      refetch: vi.fn(),
    });
    mockForm();

    render(<StorageSettings />);

    expect(screen.getByText("Storage transition failed")).toBeVisible();
    expect(screen.getByText(/Target storage could not be verified/)).toBeVisible();
    expect(screen.getByText("2 objects verified")).toBeVisible();
    expect(screen.queryByText(/private\/object-key/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Refresh" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText(/Target storage could not be verified/)).not.toBeInTheDocument();
  });

  it("shows a dismissible manual-restart alert without showing ordinary completed jobs", async () => {
    taskJobsMock.mockReturnValue({
      data: [
        {
          id: "manual-restart",
          status: "completed",
          message:
            "Storage transition committed; automatic restart unavailable — restart Silo manually",
          result_payload: { manual_restart_required: true },
        },
      ],
      isFetching: false,
      refetch: vi.fn(),
    });
    mockForm();

    render(<StorageSettings />);

    expect(screen.getByText("Manual restart required")).toBeVisible();
    expect(screen.getByText(/Restart the server to activate the new storage safely/)).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Manual restart required")).not.toBeInTheDocument();
  });

  it("ignores manual-restart wording when the structured flag is cleared", () => {
    taskJobsMock.mockReturnValue({
      data: [
        {
          id: "restarted",
          status: "completed",
          message: "restart Silo manually",
          result_payload: { manual_restart_required: false },
        },
      ],
      isFetching: false,
      refetch: vi.fn(),
    });
    mockForm();
    render(<StorageSettings />);
    expect(screen.queryByText("Manual restart required")).not.toBeInTheDocument();
  });

  it("shows static post-restart recovery state and its last error", () => {
    sourceHealthMock.mockReturnValue({
      data: {
        reachable: true,
        public_configured: true,
        public_reachable: true,
        private_configured: false,
        private_reachable: true,
        message: "",
        recovery_pending: true,
        recovery_state: "waiting_retry",
        recovery_error: "GET https://private.example.invalid/private/object-key failed",
        recovery_progress_percent: 42,
        recovery_progress_message: "Reconciliation failed for private/object-key",
      },
      isPending: false,
      isFetching: true,
      isError: false,
      refetch: vi.fn(),
    });
    mockForm();
    render(<StorageSettings />);
    expect(sourceHealthMock).toHaveBeenCalledWith(false, true);
    expect(sourceHealthMock).toHaveBeenCalledWith(true, false);
    expect(screen.getByText("Storage recovery: waiting to retry")).toBeVisible();
    expect(screen.getByText("Reconciliation paused; waiting to retry.")).toBeVisible();
    expect(screen.getByText(/Recovery needs attention/)).toBeVisible();
    expect(screen.queryByText(/private\/object-key/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Refresh" })).not.toBeInTheDocument();
  });

  it("keeps refresh stable while an active transition is background-polled", () => {
    taskJobsMock.mockReturnValue({
      data: [
        {
          id: "running",
          status: "running",
          message: "Copying private/object-key",
          progress_current: 99,
          progress_total: 100,
          result_payload: { phase: "copying", verified_objects: 2 },
        },
      ],
      isFetching: true,
      refetch: vi.fn(),
    });
    mockForm();

    render(<StorageSettings />);

    expect(screen.getByText("Storage transition: running")).toBeInTheDocument();
    expect(screen.getByText("Copying storage objects")).toBeVisible();
    expect(screen.getByText("2 objects verified")).toBeVisible();
    expect(screen.queryByText("99 / 100 objects")).not.toBeInTheDocument();
    expect(screen.queryByText(/private\/object-key/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();
  });

  it("keeps a saved credential when its input is emptied", async () => {
    const values: Record<string, string> = {
      "artwork.storage_backend": "s3",
      "s3.public_access_key": "draft",
      "s3.public_url_auth": "presigned",
    };
    const form = mockForm({
      sensitiveConfigured: ["s3.public_access_key", "s3.public_secret_key"],
      getValue: (key: string) => values[key] ?? "",
    });

    render(<StorageSettings />);

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

    render(<StorageSettings />);

    const privateGroup = within(screen.getByRole("group", { name: "Private storage" }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(privateGroup.getByLabelText("Access Key")).toHaveAttribute("type", "password"),
    );
  });

  it("fails closed when protected credential status cannot be loaded", () => {
    mockForm({ sensitiveStatusReady: false, sensitiveStatusError: true });

    render(<StorageSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Protected credential status is unavailable",
    );
    expect(screen.queryByLabelText("Access Key")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Secret Key")).not.toBeInTheDocument();
  });

  describe("staging the S3 backend", () => {
    beforeEach(() => {
      realForm.enabled = true;
      serverSettings.current = {
        "artwork.storage_backend": "local",
        "s3.public_url_auth": "presigned",
      };
      sensitiveStatus.current = { configured: [], managed_by_env: [] };
      updateSettingsMock.mockReset();
      updateSettingsMock.mockResolvedValue({ values: {}, restart_required: false });
    });

    afterEach(() => {
      realForm.enabled = false;
    });

    it("shows public storage once S3 is picked and saves its bucket", async () => {
      render(<StorageSettings />);
      expect(screen.queryByRole("group", { name: "Public storage" })).not.toBeInTheDocument();

      await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
      await userEvent.click(screen.getByRole("option", { name: "S3" }));
      expect(screen.getByRole("group", { name: "Private storage" })).toBeInTheDocument();
      const publicStorage = within(screen.getByRole("group", { name: "Public storage" }));
      await userEvent.type(publicStorage.getByLabelText("Bucket"), "artwork");
      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({
          "artwork.storage_backend": "s3",
          "s3.public_bucket": "artwork",
        }),
      );
    });

    it("opens the review on Save after S3 is picked on a locked local install", async () => {
      serverStatus.current = { artwork_storage: { backend: "local", locked: true } };
      render(<StorageSettings />);

      await userEvent.click(screen.getByRole("combobox", { name: "Backend" }));
      await userEvent.click(screen.getByRole("option", { name: "S3" }));
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      const publicStorage = within(screen.getByRole("group", { name: "Public storage" }));
      await userEvent.type(publicStorage.getByLabelText("Bucket"), "artwork");
      await userEvent.click(screen.getByRole("button", { name: "Review transition" }));

      const dialog = await screen.findByRole("dialog", { name: "Change artwork storage" });
      expect(within(dialog).getByRole("group", { name: "Target" })).toHaveTextContent("artwork");
      expect(updateSettingsMock).not.toHaveBeenCalled();
      serverStatus.current = { artwork_storage: { backend: "local", locked: false } };
    });
  });

  describe("clearing a stored credential", () => {
    beforeEach(() => {
      realForm.enabled = true;
      serverSettings.current = {
        "artwork.storage_backend": "s3",
        "s3.public_url_auth": "presigned",
      };
      sensitiveStatus.current = { configured: ["s3.public_access_key"], managed_by_env: [] };
      updateSettingsMock.mockReset();
      updateSettingsMock.mockResolvedValue({ values: {}, restart_required: false });
    });

    afterEach(() => {
      realForm.enabled = false;
    });

    it("stages the clear in the save bar and saves it as an empty value", async () => {
      render(<StorageSettings />);

      const publicGroup = within(screen.getByRole("group", { name: "Public storage" }));
      await userEvent.click(publicGroup.getByRole("button", { name: "Clear saved value" }));

      expect(screen.getByText("1 unsaved change")).toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: "Save" }));

      await waitFor(() =>
        expect(updateSettingsMock).toHaveBeenCalledWith({ "s3.public_access_key": "" }),
      );
    });

    it("takes the clear back out of the save bar when the saved value is kept", async () => {
      render(<StorageSettings />);

      const publicGroup = within(screen.getByRole("group", { name: "Public storage" }));
      await userEvent.click(publicGroup.getByRole("button", { name: "Clear saved value" }));
      await userEvent.click(publicGroup.getByRole("button", { name: "Keep saved value" }));

      expect(screen.queryByText("1 unsaved change")).not.toBeInTheDocument();
      expect(publicGroup.getByLabelText("Access Key")).toHaveAttribute(
        "placeholder",
        "••••••••••••",
      );
    });
  });
});
