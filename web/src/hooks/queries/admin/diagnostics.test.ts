import { beforeEach, describe, expect, it, vi } from "vitest";

import type { DiagnosticReport } from "@/api/types";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  update: vi.fn(),
  bundle: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({ profileId: "profile-a" }),
  isCapturedProfileAuthorityActive: () => true,
}));
vi.mock("./settings", () => ({
  useUpdateServerSetting: () => ({
    mutateAsync: async (values: unknown) => {
      try {
        return await mocks.update(values);
      } catch (error) {
        mocks.toastError((error as Error).message);
        throw error;
      }
    },
  }),
}));

vi.mock("@/api/v2/adminDiagnosticDownload", () => ({
  fetchAdminDiagnosticReportBundle: mocks.bundle,
}));

vi.mock("sonner", () => ({
  toast: {
    error: mocks.toastError,
    success: mocks.toastSuccess,
  },
}));

import { downloadDiagnosticReport } from "./diagnostics";

const report = {
  id: "83fd3186-bd4f-42e1-8285-58107c503685",
  short_id: "ABCDEF123456",
} as DiagnosticReport;

describe("downloadDiagnosticReport", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.update.mockReset();
    mocks.bundle.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("streams the bundle through the proxy download path", async () => {
    const objectURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:diagnostic");
    const revokeObjectURL = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => undefined);
    mocks.bundle.mockResolvedValue(new Blob(["bundle"], { type: "application/gzip" }));

    await downloadDiagnosticReport(report);

    expect(mocks.bundle).toHaveBeenCalledWith("83fd3186-bd4f-42e1-8285-58107c503685");
    expect(click).toHaveBeenCalledOnce();
    expect(objectURL).toHaveBeenCalledOnce();
    // The click spy records `this` as the anchor the download helper clicked;
    // assert it carried the blob URL and expected filename before cleanup.
    const clickedAnchor = click.mock.contexts[0] as HTMLAnchorElement;
    expect(clickedAnchor.href).toBe("blob:diagnostic");
    expect(clickedAnchor.download).toBe("silo-diagnostics-ABCDEF123456.tar.gz");
    expect(document.querySelector('a[download="silo-diagnostics-ABCDEF123456.tar.gz"]')).toBeNull();

    await new Promise((resolve) => window.setTimeout(resolve, 0));
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:diagnostic");
    objectURL.mockRestore();
    revokeObjectURL.mockRestore();
    click.mockRestore();
  });
});
