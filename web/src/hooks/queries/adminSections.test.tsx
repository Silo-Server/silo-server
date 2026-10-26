import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useRestoreDefaultSections, useDeleteSection } from "./sections";
const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
afterEach(() => {
  request.mockReset();
});
function setup() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3, retryDelay: 0 }, queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const hook = renderHook(
    () => ({
      restore: useRestoreDefaultSections(),
      remove: useDeleteSection(),
    }),
    { wrapper },
  );
  return { ...hook, client };
}
describe("administrator section mutation hooks", () => {
  it("does not replay restore or delete after a network failure", async () => {
    request.mockRejectedValue(new Error("Network interrupted"));
    const { result, client } = setup();
    try {
      await act(async () => {
        await expect(
          result.current.restore.mutateAsync({
            scope: "home",
            reset_profiles: true,
            etag: '"dialog"',
          }),
        ).rejects.toThrow("Network");
        await expect(
          result.current.remove.mutateAsync({ id: "s1", etag: '"delete"' }),
        ).rejects.toThrow("Network");
      });
      expect(request).toHaveBeenCalledTimes(2);
      expect(request.mock.calls.map(([, options]) => options.headers["If-Match"])).toEqual([
        '"dialog"',
        '"delete"',
      ]);
    } finally {
      client.clear();
    }
  });
});
