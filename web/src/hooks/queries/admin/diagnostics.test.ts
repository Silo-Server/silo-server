import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { DiagnosticReport } from "@/api/types";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  apiResponse: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: mocks.api,
  apiResponse: mocks.apiResponse,
}));

vi.mock("sonner", () => ({
  toast: {
    error: mocks.toastError,
    success: mocks.toastSuccess,
  },
}));

import { downloadDiagnosticReport, useUpdateDiagnosticsUploadsEnabled } from "./diagnostics";

const report = {
  id: "83fd3186-bd4f-42e1-8285-58107c503685",
  short_id: "ABCDEF123456",
} as DiagnosticReport;

describe("downloadDiagnosticReport", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.apiResponse.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("streams the bundle through the proxy download path", async () => {
    const objectURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:diagnostic");
    const revokeObjectURL = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => undefined);
    mocks.apiResponse.mockResolvedValue(
      new Response(new Blob(["bundle"]), { headers: { "Content-Type": "application/gzip" } }),
    );

    await downloadDiagnosticReport(report);

    expect(mocks.apiResponse).toHaveBeenCalledWith(
      "/admin/diagnostics/reports/83fd3186-bd4f-42e1-8285-58107c503685/download?proxy=1",
    );
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

  it("propagates request failures to the caller", async () => {
    mocks.apiResponse.mockRejectedValue(new Error("Diagnostic report bundle not found"));

    await expect(downloadDiagnosticReport(report)).rejects.toThrow(
      "Diagnostic report bundle not found",
    );
  });
});

describe("useUpdateDiagnosticsUploadsEnabled", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.apiResponse.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
  });

  function createWrapper(queryClient: QueryClient) {
    return function Wrapper({ children }: { children: ReactNode }) {
      return createElement(QueryClientProvider, { client: queryClient }, children);
    };
  }

  it.each([
    [true, "true", "Client diagnostic uploads enabled"],
    [false, "false", "Client diagnostic uploads disabled"],
  ] as const)("persists %s and refreshes diagnostics status", async (enabled, value, message) => {
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
    });
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    mocks.api.mockResolvedValue({ key: "diagnostics.uploads_enabled", value });
    const { result } = renderHook(() => useUpdateDiagnosticsUploadsEnabled(), {
      wrapper: createWrapper(queryClient),
    });

    await act(async () => {
      await result.current.mutateAsync(enabled);
    });

    expect(mocks.api).toHaveBeenCalledWith("/admin/settings/diagnostics.uploads_enabled", {
      method: "PUT",
      body: JSON.stringify({ value }),
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ["diagnostics", "status"] });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ["admin", "serverSettings"] });
    expect(mocks.toastSuccess).toHaveBeenCalledWith(message);
  });

  it("surfaces storage validation errors", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
    });
    const error = new Error("diagnostics uploads require configured private object storage");
    mocks.api.mockRejectedValue(error);
    const { result } = renderHook(() => useUpdateDiagnosticsUploadsEnabled(), {
      wrapper: createWrapper(queryClient),
    });
    let caught: unknown;

    await act(async () => {
      try {
        await result.current.mutateAsync(true);
      } catch (mutationError) {
        caught = mutationError;
      }
    });

    expect(caught).toBe(error);
    expect(mocks.toastError).toHaveBeenCalledWith(error.message);
    expect(mocks.toastSuccess).not.toHaveBeenCalled();
  });
});
