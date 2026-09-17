import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { StorageStep } from "./StorageStep";

const formMock = vi.fn();
const wizardMock = vi.fn();
vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => formMock(...args),
}));
vi.mock("../WizardContext", () => ({ useWizardContext: () => wizardMock() }));
const serverStatusMock = vi.fn(() => ({ data: undefined as unknown }));
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

function setup() {
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
    isDirty: () => false,
  });
  return { markDone, setSummary, save, setValue };
}

describe("StorageStep", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    serverStatusMock.mockReturnValue({ data: undefined });
  });

  it("locks the backend when artwork is already stored", () => {
    serverStatusMock.mockReturnValue({
      data: { artwork_storage: { backend: "local", locked: true } },
    });
    setup();
    render(<StorageStep />);
    expect(screen.getByRole("combobox", { name: "Storage" })).toBeDisabled();
    expect(screen.getByText(/Locked: files have already been stored/)).toBeInTheDocument();
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
