import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { StorageStep } from "./StorageStep";

const formMock = vi.fn();
const wizardMock = vi.fn();
vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => formMock(...args),
}));
vi.mock("../WizardContext", () => ({ useWizardContext: () => wizardMock() }));
const serverStatusMock = vi.fn(() => ({
  data: { artwork_storage: { backend: "local", locked: false, status_known: true } } as unknown,
  isPending: false,
  isError: false,
}));
vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerStatus: () => serverStatusMock(),
  useCheckAdminSettingsConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

window.HTMLElement.prototype.hasPointerCapture ??= () => false;
window.HTMLElement.prototype.scrollIntoView ??= () => {};

function setup({ dirtyKeys = [] }: { dirtyKeys?: string[] } = {}) {
  const values: Record<string, string> = {};
  const markDone = vi.fn();
  const setSummary = vi.fn();
  const save = vi.fn().mockResolvedValue(undefined);
  const setValue = vi.fn((key: string, value: string) => {
    values[key] = value;
  });
  wizardMock.mockReturnValue({ markDone, setSummary });
  formMock.mockReturnValue({
    isPending: false,
    buildConnectionCheckRequest: () => ({}),
    getValue: (key: string) => values[key] ?? "",
    setValue,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
    dirtyCount: 1,
    isSaving: false,
    save,
    isDirty: (key: string) => dirtyKeys.includes(key),
  });
  return { markDone, setSummary, save, setValue };
}

describe("StorageStep", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    serverStatusMock.mockReturnValue({
      data: { artwork_storage: { backend: "local", locked: false, status_known: true } },
      isPending: false,
      isError: false,
    });
  });

  it.each([
    ["loading", true, false],
    ["failed", false, true],
  ])("holds storage editing while lock status is %s", (state, isPending, isError) => {
    serverStatusMock.mockReturnValue({ data: undefined, isPending, isError });
    setup();

    render(<StorageStep />);

    expect(screen.queryByRole("combobox", { name: "Storage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue/ })).not.toBeInTheDocument();
    expect(
      state === "failed"
        ? screen.getByRole("alert")
        : screen.getByRole("status", { name: "Loading" }),
    ).toBeInTheDocument();
  });

  it("holds storage editing when the status response has no lock details", () => {
    serverStatusMock.mockReturnValue({ data: {}, isPending: false, isError: false });
    setup();

    render(<StorageStep />);

    expect(screen.getByRole("alert")).toHaveTextContent("Storage lock status is unavailable");
    expect(screen.queryByRole("combobox", { name: "Storage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Continue/ })).not.toBeInTheDocument();
  });

  it.each([
    ["cached status after a failed refresh", true, true],
    ["unknown lock status", false, false],
    ["an older status response", undefined, false],
  ])("keeps other settings usable with %s", async (_, statusKnown, isError) => {
    serverStatusMock.mockReturnValue({
      data: {
        artwork_storage: {
          backend: "local",
          locked: false,
          ...(statusKnown === undefined ? {} : { status_known: statusKnown }),
        },
      },
      isPending: false,
      isError,
    });
    const { save, setValue } = setup();
    render(<StorageStep />);

    expect(screen.getByRole("alert")).toHaveTextContent("Storage lock status is unavailable");
    expect(screen.getByRole("combobox", { name: "Storage" })).toBeDisabled();
    expect(screen.getByLabelText("Local storage path")).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Keep provider artwork" })).toBeEnabled();

    const toggles = screen.getAllByRole("button", { name: "Set up" });
    await userEvent.click(toggles[1]!);
    expect(screen.getByLabelText("Endpoint")).toBeDisabled();
    expect(screen.getByLabelText("Bucket")).toBeDisabled();
    expect(screen.getByLabelText("Folder inside the bucket")).toBeDisabled();
    expect(screen.getByLabelText("Access key")).toBeEnabled();

    await userEvent.click(screen.getByRole("switch", { name: "Keep provider artwork" }));
    expect(setValue).toHaveBeenCalledWith("metadata.cache_images", "false");
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await waitFor(() => expect(save).toHaveBeenCalledOnce());
  });

  it("holds Continue when a location edit was staged before lock status became unknown", () => {
    serverStatusMock.mockReturnValue({
      data: { artwork_storage: { backend: "local", locked: false, status_known: false } },
      isPending: false,
      isError: false,
    });
    const { markDone, save } = setup({ dirtyKeys: ["artwork.local_path"] });
    render(<StorageStep />);

    expect(screen.getByRole("button", { name: /Continue/ })).toBeDisabled();
    expect(screen.getByRole("switch", { name: "Keep provider artwork" })).toBeEnabled();
    fireEvent.submit(screen.getByRole("button", { name: /Continue/ }).closest("form")!);
    expect(save).not.toHaveBeenCalled();
    expect(markDone).not.toHaveBeenCalled();
  });

  it("locks the backend when artwork is already stored", () => {
    serverStatusMock.mockReturnValue({
      data: { artwork_storage: { backend: "local", locked: true, status_known: true } },
      isPending: false,
      isError: false,
    });
    setup();
    render(<StorageStep />);
    expect(screen.getByRole("combobox", { name: "Storage" })).toBeDisabled();
    expect(screen.getByText(/Locked: files have already been stored/)).toBeInTheDocument();
  });

  // After the first scan the server refuses a new private location with 409;
  // the wizard has no transition flow, so the location fields are read-only.
  it("locks the private bucket location once files are stored", async () => {
    serverStatusMock.mockReturnValue({
      data: { artwork_storage: { backend: "local", locked: true, status_known: true } },
      isPending: false,
      isError: false,
    });
    setup();
    render(<StorageStep />);
    // Redis, public S3, and the private bucket each have a "Set up" toggle.
    const toggles = screen.getAllByRole("button", { name: "Set up" });
    await userEvent.click(toggles[toggles.length - 1]!);
    expect(screen.getByLabelText("Bucket")).toBeDisabled();
    expect(screen.getByLabelText("Endpoint")).toBeDisabled();
    expect(screen.getByLabelText("Folder inside the bucket")).toBeDisabled();
    expect(screen.getByLabelText("Access key")).toBeEnabled();
    expect(
      screen.getByText(/Silo records a configured private bucket at startup/),
    ).toBeInTheDocument();
  });

  // Automatic storage on local disk keeps the public bucket empty; adding one
  // would switch where files are stored, so it is locked for that reason.
  it("explains the public bucket lock for Automatic storage on local disk", async () => {
    serverStatusMock.mockReturnValue({
      data: { artwork_storage: { backend: "local", locked: true, status_known: true } },
      isPending: false,
      isError: false,
    });
    setup();
    render(<StorageStep />);
    const toggles = screen.getAllByRole("button", { name: "Set up" });
    await userEvent.click(toggles[1]!);
    expect(screen.getByLabelText("Bucket")).toBeDisabled();
    expect(
      screen.getByText(/adding a public bucket would switch Automatic storage to S3/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/files are stored here/)).not.toBeInTheDocument();
  });

  it("locks only the private fields when an empty private bucket is recorded", async () => {
    serverStatusMock.mockReturnValue({
      data: {
        artwork_storage: {
          backend: "local",
          locked: false,
          private_locked: true,
          status_known: true,
        },
      },
      isPending: false,
      isError: false,
    });
    setup();
    render(<StorageStep />);
    expect(screen.getByRole("combobox", { name: "Storage" })).toBeEnabled();
    const toggles = screen.getAllByRole("button", { name: "Set up" });
    await userEvent.click(toggles[toggles.length - 1]!);
    expect(screen.getByLabelText("Bucket")).toBeDisabled();
    expect(screen.getByText(/even when empty/)).toBeInTheDocument();
    expect(screen.queryByText(/files are stored here/)).not.toBeInTheDocument();
  });

  it("completes without S3 and reports local storage", async () => {
    const { markDone, setSummary, save } = setup();
    render(<StorageStep />);
    expect(screen.queryByLabelText("Bucket")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Local storage path")).toBeEnabled();
    expect(setSummary).toHaveBeenCalledWith("storage", "Local storage");
    await userEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await waitFor(() => expect(markDone).toHaveBeenCalledWith("storage"));
    expect(save).toHaveBeenCalledOnce();
  });

  it("reveals S3 fields and stages the backend selection", async () => {
    const { setValue } = setup();
    render(<StorageStep />);
    await userEvent.click(screen.getByRole("combobox", { name: "Storage" }));
    await userEvent.click(screen.getByRole("option", { name: "S3" }));
    expect(screen.getByLabelText("Bucket")).toBeInTheDocument();
    expect(setValue).toHaveBeenCalledWith("artwork.storage_backend", "s3");
    expect(formMock.mock.calls[0]?.[0].keys).toContain("artwork.storage_backend");
  });
});
