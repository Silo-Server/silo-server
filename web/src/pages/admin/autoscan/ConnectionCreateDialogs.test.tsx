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
  const res = new Response(null, { status: 201, headers: { "Content-Type": "application/json" } });
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
function fill() {
  fireEvent.change(screen.getByPlaceholderText("http://localhost:8989"), {
    target: { value: "https://example.invalid" },
  });
}
function fillCreation() {
  fireEvent.change(screen.getByPlaceholderText("My Sonarr"), { target: { value: "Synthetic" } });
  fill();
  fireEvent.change(screen.getByPlaceholderText("Enter API key"), {
    target: { value: "private-key" },
  });
}
it("closes the active connection draft after acknowledged creation", async () => {
  const d = deferred();
  fixture(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  fillCreation();
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  await d.ready();
  await d.finish();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
it("retains a newer connection draft after late creation acknowledgement", async () => {
  const d = deferred();
  fixture(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  fillCreation();
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  await d.ready();
  fireEvent.change(screen.getByPlaceholderText("My Sonarr"), { target: { value: "New draft" } });
  await d.finish();
  expect(screen.getByDisplayValue("New draft")).toBeTruthy();
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
it.each([false, true])(
  "inline creation only selects the unchanged draft: changed=%s",
  async (changed) => {
    const d = deferred();
    const onChange = vi.fn();
    fixture(
      <InlineConnectionPicker
        value=""
        onChange={onChange}
        options={[]}
        required={false}
        connectionKinds={["sonarr"]}
      />,
    );
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "ArrowDown" });
    fireEvent.click(await screen.findByRole("option", { name: "+ Add a server…" }));
    fillCreation();
    fireEvent.click(screen.getByRole("button", { name: "Add server" }));
    await d.ready();
    if (changed)
      fireEvent.change(screen.getByPlaceholderText("My Sonarr"), {
        target: { value: "New draft" },
      });
    await d.finish();
    if (changed) {
      expect(onChange).not.toHaveBeenCalled();
      expect(screen.getByDisplayValue("New draft")).toBeTruthy();
    } else {
      expect(onChange).toHaveBeenCalledExactlyOnceWith("created");
    }
    expect(d.fetchMock).toHaveBeenCalledOnce();
  },
);
