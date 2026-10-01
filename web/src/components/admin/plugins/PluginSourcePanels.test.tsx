import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { PluginRepositoriesPanel, PluginUploadPanel } from "./PluginSourcePanels";

const createRepositoryMock = vi.fn();
let createPending = false;
let uploadPending = false;

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useCreatePluginRepository: () => ({ mutate: createRepositoryMock, isPending: createPending }),
  useUpdatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  usePluginUpload: () => ({ upload: vi.fn(), progress: null, isPending: uploadPending }),
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

  it("does not claim the repository list is empty when loading failed", () => {
    render(
      <PluginRepositoriesPanel repositories={[]} repositoriesError={new Error("unavailable")} />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Failed to load plugin repositories.");
    expect(screen.queryByText(/No repositories configured/)).not.toBeInTheDocument();
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

describe("PluginUploadPanel", () => {
  beforeEach(() => {
    uploadPending = false;
  });
  it("keeps the file input in the tab order", () => {
    render(<PluginUploadPanel />);
    const input = screen.getByLabelText("Plugin file");
    expect(input).toHaveAttribute("type", "file");
    expect(input).not.toHaveClass("hidden");
    expect(input).toHaveClass("sr-only");
  });

  it("locks the picker while an upload is pending", () => {
    uploadPending = true;
    render(<PluginUploadPanel />);
    expect(screen.getByLabelText("Plugin file")).toBeDisabled();
  });
});
