import { fireEvent, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import {
  CREATED_CONNECTION,
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
    useUpdateAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
    useDeleteAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
  };
});
setupAutoscanAdmin({ profileId: "profile-a", pointerCapture: true });
function fillCreation() {
  fireEvent.change(screen.getByPlaceholderText("My Sonarr"), { target: { value: "Synthetic" } });
  fillConnectionURL();
  fireEvent.change(screen.getByPlaceholderText("Enter API key"), {
    target: { value: "private-key" },
  });
}
it("closes the active connection draft after acknowledged creation", async () => {
  const d = deferredJSON(CREATED_CONNECTION, 201);
  renderWithClient(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  fillCreation();
  fireEvent.click(screen.getByRole("button", { name: "Add connection" }));
  await d.ready();
  await d.finish();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
it("retains a newer connection draft after late creation acknowledgement", async () => {
  const d = deferredJSON(CREATED_CONNECTION, 201);
  renderWithClient(<ConnectionsPanel />);
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
    const d = deferredJSON(CREATED_CONNECTION, 201);
    const onChange = vi.fn();
    await openInlineConnectionDialog(onChange);
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
