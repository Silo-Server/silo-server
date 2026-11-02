import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { PluginRepositoriesPanel } from "./PluginSourcePanels";

const createRepositoryMock = vi.fn();
let createPending = false;

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useCreatePluginRepository: () => ({ mutate: createRepositoryMock, isPending: createPending }),
  useUpdatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  usePluginUpload: () => ({ upload: vi.fn(), progress: null, isPending: false }),
}));

describe("PluginRepositoriesPanel", () => {
  beforeEach(() => {
    createRepositoryMock.mockReset();
    createPending = false;
  });

  it("keeps the typed repository until the server accepts it", () => {
    render(<PluginRepositoriesPanel repositories={[]} repositoriesError={null} />);
    fireEvent.click(screen.getByRole("button", { name: "Add repository" }));
    fireEvent.change(screen.getByLabelText("Repository name"), { target: { value: "Home lab" } });
    fireEvent.change(screen.getByLabelText("Repository URL"), {
      target: { value: "https://plugins.example.test/index.json" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));

    expect(createRepositoryMock).toHaveBeenCalledWith(
      {
        display_name: "Home lab",
        url: "https://plugins.example.test/index.json",
        enabled: true,
      },
      { onSuccess: expect.any(Function) },
    );
    // Not accepted yet: a rejected URL can still be corrected.
    expect(screen.getByLabelText("Repository URL")).toHaveValue(
      "https://plugins.example.test/index.json",
    );
  });

  it("prevents duplicate repository submissions while creation is pending", () => {
    createPending = true;
    render(<PluginRepositoriesPanel repositories={[]} repositoriesError={null} />);
    fireEvent.click(screen.getByRole("button", { name: "Add repository" }));
    fireEvent.change(screen.getByLabelText("Repository name"), { target: { value: "Home lab" } });
    fireEvent.change(screen.getByLabelText("Repository URL"), {
      target: { value: "https://plugins.example.test/index.json" },
    });
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
    fireEvent.submit(screen.getByRole("button", { name: "Add" }).closest("form")!);
    expect(createRepositoryMock).not.toHaveBeenCalled();
  });
});
