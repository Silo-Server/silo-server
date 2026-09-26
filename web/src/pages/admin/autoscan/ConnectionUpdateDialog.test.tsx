import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    useDeleteAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
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
function deferred() {
  let release!: (s: string) => void;
  const res = new Response(null, { status: 200, headers: { "Content-Type": "application/json" } });
  res.text = () =>
    new Promise((resolve) => {
      release = resolve;
    });
  const fetchMock = vi.fn().mockResolvedValue(res);
  vi.stubGlobal("fetch", fetchMock);
  return {
    fetchMock,
    ready: () => waitFor(() => expect(release).toBeTypeOf("function")),
    finish: () =>
      act(async () =>
        release(
          JSON.stringify({ id: "created", name: "Synthetic", kind: "sonarr", has_api_key: true }),
        ),
      ),
  };
}
it("closes the active connection draft after acknowledged update", async () => {
  const d = deferred();
  fixture(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Edit connection" }));
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await d.ready();
  await d.finish();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
it("retains a newer connection draft after late update acknowledgement", async () => {
  const d = deferred();
  fixture(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Edit connection" }));
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await d.ready();
  fireEvent.change(screen.getByPlaceholderText("My Sonarr"), { target: { value: "New draft" } });
  await d.finish();
  expect(screen.getByDisplayValue("New draft")).toBeTruthy();
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
