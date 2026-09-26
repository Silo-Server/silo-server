import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import ConnectionsPanel from "./ConnectionsPanel";
import { InlineConnectionPicker } from "./InlineConnectionPicker";

vi.mock("@/hooks/queries/useRequests", () => ({ useRequestIntegrations: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/useAutoscan", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/queries/useAutoscan")>();
  return {
    ...actual,
    useAutoscanConnections: () => ({ data: [], isLoading: false }),
    useCreateAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
    useUpdateAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
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
  const res = new Response(null, { headers: { "Content-Type": "application/json" } });
  res.text = () =>
    new Promise((resolve) => {
      release = resolve;
    });
  const fetchMock = vi.fn().mockResolvedValue(res);
  vi.stubGlobal("fetch", fetchMock);
  return {
    fetchMock,
    ready: () => waitFor(() => expect(release).toBeTypeOf("function")),
    finish: () => act(async () => release(JSON.stringify({ ok: true, version: "4.0" }))),
  };
}
function fill() {
  fireEvent.change(screen.getByPlaceholderText("http://localhost:8989"), {
    target: { value: "https://example.invalid" },
  });
}
it("shows a successful check for the active connection dialog draft", async () => {
  const d = deferred();
  fixture(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  fill();
  fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
  await d.ready();
  await d.finish();
  expect(await screen.findByText("Connected (v4.0)")).toBeTruthy();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
it.each(["edit", "reopen"])("discards late connection dialog result after %s", async (change) => {
  const d = deferred();
  fixture(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  fill();
  fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
  await d.ready();
  if (change === "edit") {
    fireEvent.change(screen.getByPlaceholderText("http://localhost:8989"), {
      target: { value: "https://other.invalid" },
    });
  } else {
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  }
  await d.finish();
  expect(screen.queryByText("Connected (v4.0)")).toBeNull();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
it("discards the inline picker result when its draft changes", async () => {
  const d = deferred();
  fixture(
    <InlineConnectionPicker
      value=""
      onChange={vi.fn()}
      options={[]}
      required={false}
      connectionKinds={["sonarr"]}
    />,
  );
  fireEvent.keyDown(screen.getByRole("combobox"), { key: "ArrowDown" });
  fireEvent.click(await screen.findByRole("option", { name: "+ Add a server…" }));
  fill();
  fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
  await d.ready();
  fireEvent.change(screen.getByPlaceholderText("http://localhost:8989"), {
    target: { value: "https://other.invalid" },
  });
  await d.finish();
  expect(screen.queryByText("Connected (v4.0)")).toBeNull();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
