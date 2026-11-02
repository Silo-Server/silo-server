import { fireEvent, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { renderWithClient, setupAutoscanAdmin } from "./autoscanTestSession.test-support";
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
    useUpdateAutoscanConnection: () => ({ mutate: vi.fn(), isPending: false }),
  };
});
setupAutoscanAdmin({ profileId: "profile-a", pointerCapture: true });
it("confirms one delete and explains the existing source restriction", async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetchMock);
  renderWithClient(<ConnectionsPanel />);
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
