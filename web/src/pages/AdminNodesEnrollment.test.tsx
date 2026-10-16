import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import AdminNodes from "./AdminNodes";

const mutations = vi.hoisted(() => ({ create: vi.fn(), enroll: vi.fn(), update: vi.fn() }));
vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: () => ({ data: [], isLoading: false }),
  useCreateNode: () => ({ mutate: mutations.create, isPending: false }),
  useEnrollNode: () => ({ mutate: mutations.enroll, isPending: false }),
  useUpdateNode: () => ({ mutate: mutations.update, isPending: false }),
  useDeleteNode: () => ({ mutate: vi.fn(), isPending: false }),
  useCheckNodeHealth: () => ({ mutate: vi.fn(), isPending: false }),
  useReprobeNode: () => ({ mutate: vi.fn(), isPending: false }),
  useSetNodeDrain: () => ({ mutate: vi.fn(), isPending: false }),
  useToggleNode: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/admin/settings", () => ({ useAdminServerSettings: () => ({ data: {} }) }));
vi.mock("@/pages/admin-settings/NodeRoutingSettings", () => ({ NodeRoutingSettings: () => null }));
beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);
function openForm(type: "Transcode" | "Proxy") {
  render(<AdminNodes />);
  fireEvent.click(screen.getAllByRole("button", { name: `Add ${type}` })[0]!);
  const dialog = screen.getByRole("dialog");
  const inputs = within(dialog).getAllByRole("textbox");
  fireEvent.change(inputs[0]!, { target: { value: "Sold node" } });
  fireEvent.change(inputs[1]!, { target: { value: "http://worker.internal:8082" } });
  return dialog;
}
it("hands a new transcode node token and connection overrides to enrollment", () => {
  const dialog = openForm("Transcode");
  const token = within(dialog).getByLabelText("Enrollment token");
  expect(token).toHaveAttribute("type", "password");
  fireEvent.change(token, { target: { value: "provider-token-at-least-32-characters" } });
  fireEvent.click(within(dialog).getByText("Worker connection settings"));
  fireEvent.change(within(dialog).getByLabelText("Worker database URL"), {
    target: { value: "postgres://db.internal/silo" },
  });
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
  expect(mutations.enroll).toHaveBeenCalledWith(
    expect.objectContaining({
      type: "transcode",
      url: "http://worker.internal:8082",
      enrollment_token: "provider-token-at-least-32-characters",
      worker_database_url: "postgres://db.internal/silo",
    }),
    expect.any(Object),
  );
  expect(mutations.create).not.toHaveBeenCalled();
});
it("preserves configuration-only creation when no token is supplied", () => {
  const dialog = openForm("Transcode");
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
  expect(mutations.create).toHaveBeenCalledWith(
    expect.objectContaining({ type: "transcode" }),
    expect.any(Object),
  );
  expect(mutations.enroll).not.toHaveBeenCalled();
});
it("does not offer enrollment for a proxy node", () => {
  const dialog = openForm("Proxy");
  expect(within(dialog).queryByLabelText("Enrollment token")).not.toBeInTheDocument();
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
  expect(mutations.create).toHaveBeenCalledWith(
    expect.objectContaining({ type: "proxy" }),
    expect.any(Object),
  );
  expect(mutations.enroll).not.toHaveBeenCalled();
});
