import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import ConnectionsPanel from "./ConnectionsPanel";

vi.mock("@/hooks/queries/useRequests", () => ({ useRequestIntegrations: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/useAutoscan", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/queries/useAutoscan")>();
  return {
    ...actual,
    useAutoscanConnections: () => ({
      data: [
        {
          id: "connection-a",
          name: "Synthetic",
          kind: "sonarr",
          base_url: "https://example.invalid",
          has_api_key: true,
        },
      ],
      isLoading: false,
    }),
    useCreateAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
    useUpdateAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
  };
});
const browserMethods = [
  "hasPointerCapture",
  "setPointerCapture",
  "releasePointerCapture",
  "scrollIntoView",
] as const;
const originalMethods = browserMethods.map((name) =>
  Object.getOwnPropertyDescriptor(HTMLElement.prototype, name),
);
beforeEach(() => {
  vi.stubGlobal("PointerEvent", MouseEvent);
  for (const name of browserMethods)
    Object.defineProperty(HTMLElement.prototype, name, {
      configurable: true,
      value: name === "hasPointerCapture" ? () => false : () => {},
    });
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  browserMethods.forEach((name, index) => {
    const original = originalMethods[index];
    if (original) Object.defineProperty(HTMLElement.prototype, name, original);
    else Reflect.deleteProperty(HTMLElement.prototype, name);
  });
  vi.unstubAllGlobals();
});
function fixture(component: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  render(<QueryClientProvider client={client}>{component}</QueryClientProvider>);
}
it("confirms one delete and explains the existing source restriction", async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);
  fixture(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Delete connection" }));
  expect(screen.getByText(/Deletion is blocked while scan sources reference/)).toBeTruthy();
  expect(fetchMock).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: /^Delete$/ }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  expect(fetchMock.mock.calls[0]?.[1].method).toBe("DELETE");
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/api/v2/admin/autoscan/connections/connection-a",
  );
  await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
});
