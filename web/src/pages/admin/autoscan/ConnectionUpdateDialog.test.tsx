import { fireEvent, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import {
  CREATED_CONNECTION,
  deferredJSON,
  renderWithClient,
  setupAutoscanAdmin,
} from "./autoscanTestSession.test-support";
import ConnectionsPanel from "./ConnectionsPanel";

vi.mock("@/hooks/queries/admin/requests", () => ({ useRequestIntegrations: () => ({ data: [] }) }));
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
setupAutoscanAdmin({ profileId: "profile-a", pointerCapture: true });
it("retains a newer connection draft after late update acknowledgement", async () => {
  const d = deferredJSON(CREATED_CONNECTION);
  renderWithClient(<ConnectionsPanel />);
  fireEvent.click(screen.getByRole("button", { name: "Edit connection" }));
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await d.ready();
  fireEvent.change(screen.getByPlaceholderText("My Sonarr"), { target: { value: "New draft" } });
  await d.finish();
  expect(screen.getByDisplayValue("New draft")).toBeTruthy();
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(d.fetchMock).toHaveBeenCalledOnce();
});
