import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ServerStorageStep } from "./ServerStorageStep";

const useSettingsFormMock = vi.fn();
const useWizardContextMock = vi.fn();
const useCheckAdminSettingsConnectionMock = vi.fn();
const useJellyfinCompatStatusMock = vi.fn();
const useInstallJellyfinCompatWebMock = vi.fn();
const useQueryMock = vi.fn();

vi.mock("@tanstack/react-query", () => ({
  useQuery: (...args: unknown[]) => useQueryMock(...args),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("../WizardContext", () => ({
  useWizardContext: (...args: unknown[]) => useWizardContextMock(...args),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useCheckAdminSettingsConnection: (...args: unknown[]) =>
    useCheckAdminSettingsConnectionMock(...args),
  useJellyfinCompatStatus: (...args: unknown[]) => useJellyfinCompatStatusMock(...args),
  useInstallJellyfinCompatWeb: (...args: unknown[]) => useInstallJellyfinCompatWebMock(...args),
}));

const defaultValues: Record<string, string> = {
  "s3.public_url_auth": "presigned",
  "jellyfin_compat.enabled": "true",
  "jellyfin_compat.web_version": "",
};

interface MockStepOptions {
  dirtyCount?: number;
  dirtyKeys?: string[];
  installMutateAsync?: ReturnType<typeof vi.fn>;
  jellyfinStatus?: Record<string, unknown>;
  markDone?: ReturnType<typeof vi.fn>;
  save?: ReturnType<typeof vi.fn>;
  values?: Record<string, string>;
}

function mockStep({
  dirtyCount = 0,
  dirtyKeys = [],
  installMutateAsync = vi.fn().mockResolvedValue({}),
  jellyfinStatus = {
    web_state: "missing",
    installer_ready: true,
    prerequisites: [],
  },
  markDone = vi.fn(),
  save = vi.fn().mockResolvedValue(undefined),
  values = {},
}: MockStepOptions = {}) {
  const formValues = { ...defaultValues, ...values };

  useWizardContextMock.mockReturnValue({ markDone });
  useQueryMock.mockReturnValue({ data: null });
  useCheckAdminSettingsConnectionMock.mockReturnValue({
    isPending: false,
    mutateAsync: vi.fn(),
  });
  useJellyfinCompatStatusMock.mockReturnValue({
    data: jellyfinStatus,
  });
  useInstallJellyfinCompatWebMock.mockReturnValue({
    isPending: false,
    mutateAsync: installMutateAsync,
  });
  useSettingsFormMock.mockReturnValue({
    isLoading: false,
    getValue: (key: string) => formValues[key] ?? "",
    setValue: vi.fn((key: string, value: string) => {
      formValues[key] = value;
    }),
    dirtyCount,
    dirtyKeys,
    save,
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    buildConnectionCheckRequest: vi.fn(),
  });

  return { installMutateAsync, markDone, save };
}

describe("ServerStorageStep", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders connection check actions for Redis and public/private S3 storage", () => {
    mockStep();

    const markup = renderToStaticMarkup(<ServerStorageStep />);

    expect(markup).toContain("Public Assets Storage");
    expect(markup).toContain("Private Internal Storage");
    expect(markup).toContain("Check Connection");
    expect(markup).toContain("API layer");
    expect(markup).toContain("Web UI layer");
    expect(markup).toContain("Pinned Web version");
    expect(markup).toContain("Web install directory");
    expect(markup).toContain("/var/lib/silo/compat/jellyfin-web");
  });

  it("uses Jellyfin runtime status when the explicit enabled setting is missing", () => {
    mockStep({
      values: { "jellyfin_compat.enabled": "" },
      jellyfinStatus: {
        enabled: true,
        web_state: "missing",
        installer_ready: true,
        prerequisites: [],
      },
    });

    render(<ServerStorageStep />);

    expect(screen.getByRole("switch", { name: "Enable Jellyfin-compatible API" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    expect(screen.getByRole("button", { name: "Install Web UI" })).toBeEnabled();
  });

  it("waits for the queued Jellyfin Web install request to be accepted before continuing", async () => {
    let acceptInstall: () => void = () => {};
    const installAccepted = new Promise((resolve) => {
      acceptInstall = () => resolve({});
    });
    const installMutateAsync = vi.fn(() => installAccepted);
    const { markDone } = mockStep({ installMutateAsync });

    render(<ServerStorageStep />);

    await userEvent.click(screen.getByRole("button", { name: "Install Web UI" }));
    expect(screen.getByRole("button", { name: "Web UI will be installed" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save & continue" }));

    expect(installMutateAsync).toHaveBeenCalledWith({});
    expect(markDone).not.toHaveBeenCalled();

    acceptInstall();
    await waitFor(() => expect(markDone).toHaveBeenCalledWith("server"));
  });

  it("does not continue when the queued Jellyfin Web install request is rejected", async () => {
    const installMutateAsync = vi.fn().mockRejectedValue(new Error("missing prerequisite"));
    const { markDone } = mockStep({ installMutateAsync });

    render(<ServerStorageStep />);

    await userEvent.click(screen.getByRole("button", { name: "Install Web UI" }));
    await userEvent.click(screen.getByRole("button", { name: "Save & continue" }));

    expect(installMutateAsync).toHaveBeenCalledWith({});
    await waitFor(() => expect(installMutateAsync).toHaveBeenCalled());
    expect(markDone).not.toHaveBeenCalled();
  });
});
