import { fireEvent, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import {
  deferredJSON,
  fillConnectionURL,
  openInlineConnectionDialog,
  renderWithClient,
  setupAutoscanAdmin,
} from "./autoscanTestSession.test-support";
import ConnectionsPanel from "./ConnectionsPanel";

vi.mock("@/hooks/queries/admin/requests", () => ({ useRequestIntegrations: () => ({ data: [] }) }));
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
setupAutoscanAdmin({ profileId: "profile-a", pointerCapture: true });
it("shows a successful check for the active connection dialog draft", async () => {
  const d = deferredJSON({ ok: true, version: "4.0" });
  renderWithClient(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  fillConnectionURL();
  fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
  await d.ready();
  await d.finish();
  expect(await screen.findByText("Connected (v4.0)")).toBeTruthy();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
it.each(["edit", "reopen"])("discards late connection dialog result after %s", async (change) => {
  const d = deferredJSON({ ok: true, version: "4.0" });
  renderWithClient(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  fillConnectionURL();
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
  const d = deferredJSON({ ok: true, version: "4.0" });
  await openInlineConnectionDialog();
  fillConnectionURL();
  fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
  await d.ready();
  fireEvent.change(screen.getByPlaceholderText("http://localhost:8989"), {
    target: { value: "https://other.invalid" },
  });
  await d.finish();
  expect(screen.queryByText("Connected (v4.0)")).toBeNull();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
