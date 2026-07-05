import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { useSetSetting, type SettingsMap } from "./settings";
import { settingsKeys } from "./keys";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", () => ({ api: apiMock }));

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
}

describe("useSetSetting overlapping mutations", () => {
  it("does not roll back a newer successful save of the same key when an older save fails", async () => {
    const { queryClient, wrapper } = createHarness();
    queryClient.setQueryData<SettingsMap>(settingsKeys.list(), { "ui.date_format": "auto" });

    let failFirst: (err: Error) => void = () => {};
    let resolveSecond: (value: unknown) => void = () => {};
    apiMock
      .mockImplementationOnce(
        () =>
          new Promise((_resolve, reject) => {
            failFirst = reject;
          }),
      )
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveSecond = resolve;
          }),
      );

    const { result } = renderHook(() => useSetSetting(), { wrapper });

    result.current.mutate({ key: "ui.date_format", value: "DD/MM/YYYY" });
    result.current.mutate({ key: "ui.date_format", value: "YYYY-MM-DD" });
    await waitFor(() => expect(apiMock).toHaveBeenCalledTimes(2));

    resolveSecond(undefined);
    failFirst(new Error("network"));

    await waitFor(() => {
      const list = queryClient.getQueryData<SettingsMap>(settingsKeys.list());
      // The failed older save must not resurrect "auto" over the accepted value.
      expect(list?.["ui.date_format"]).toBe("YYYY-MM-DD");
    });
  });

  it("rolls back the optimistic value when a lone save fails", async () => {
    const { queryClient, wrapper } = createHarness();
    queryClient.setQueryData<SettingsMap>(settingsKeys.list(), { "ui.time_format": "12h" });
    apiMock.mockImplementationOnce(() => Promise.reject(new Error("network")));

    const { result } = renderHook(() => useSetSetting(), { wrapper });
    result.current.mutate({ key: "ui.time_format", value: "24h" });

    await waitFor(() => {
      const list = queryClient.getQueryData<SettingsMap>(settingsKeys.list());
      expect(list?.["ui.time_format"]).toBe("12h");
    });
  });
});
