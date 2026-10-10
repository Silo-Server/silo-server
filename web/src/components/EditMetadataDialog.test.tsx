import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  update: vi.fn(),
  updateAsync: vi.fn(),
  refresh: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useUpdateItemMetadata: () => ({
    mutate: mocks.update,
    mutateAsync: mocks.updateAsync,
    isPending: false,
  }),
  useRefreshItemMetadata: () => ({ mutate: mocks.refresh }),
}));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => true }));
vi.mock("@/components/MetadataTranslatePanel", () => ({ MetadataTranslatePanel: () => null }));
vi.mock("@/components/ConfirmDialog", () => ({
  ConfirmDialog: ({
    open,
    confirmLabel,
    onConfirm,
  }: {
    open: boolean;
    confirmLabel: string;
    onConfirm: () => void;
  }) => (open ? <button onClick={onConfirm}>{confirmLabel}</button> : null),
}));
vi.mock("@/components/ImageSelectorTab", () => ({
  default: ({
    onImageApplied,
    onApplyPendingChange,
  }: {
    onImageApplied?: () => void;
    onApplyPendingChange?: (pending: boolean) => void;
  }) => (
    <>
      <button onClick={onImageApplied}>Apply image</button>
      <button onClick={() => onApplyPendingChange?.(true)}>Begin image apply</button>
      <button onClick={() => onApplyPendingChange?.(false)}>Finish image apply</button>
    </>
  ),
}));

import EditMetadataDialog from "./EditMetadataDialog";

function series(lockedFields: number[] = []): ItemDetail {
  return {
    content_id: "series-test",
    type: "series",
    title: "Test Series",
    locked_fields: lockedFields,
  } as ItemDetail;
}

describe("EditMetadataDialog image locks", () => {
  beforeEach(() => {
    mocks.update.mockReset();
    mocks.refresh.mockReset();
  });

  it("keeps a newly fetched image lock when other metadata is saved", () => {
    const onOpenChange = vi.fn();
    const { rerender } = render(
      <EditMetadataDialog item={series()} open onOpenChange={onOpenChange} />,
    );
    rerender(<EditMetadataDialog item={series([10])} open onOpenChange={onOpenChange} />);

    fireEvent.change(screen.getByDisplayValue("Test Series"), {
      target: { value: "Updated Series" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      { title: "Updated Series", locked_fields: [0, 10] },
      expect.any(Object),
    );
  });

  it("includes the image lock when saving before the item has refetched", () => {
    render(<EditMetadataDialog item={series()} open onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Images" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply image" }));
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith({ locked_fields: [10] }, expect.any(Object));
  });

  it("waits for an image apply to finish before saving metadata", () => {
    render(<EditMetadataDialog item={series()} open onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Begin image apply" }));
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Finish image apply" }));
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeEnabled();
  });
});

describe("EditMetadataDialog field locks", () => {
  beforeEach(() => {
    mocks.update.mockReset();
    mocks.updateAsync.mockReset();
    mocks.refresh.mockReset();
  });

  it("locks the overview when only the tagline changes", () => {
    render(<EditMetadataDialog item={series()} open onOpenChange={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText("No tagline"), {
      target: { value: "Edited tagline" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save Changes" }));

    expect(mocks.update).toHaveBeenCalledWith(
      { tagline: "Edited tagline", locked_fields: [1] },
      expect.any(Object),
    );
  });

  it("does not offer a series status field", () => {
    render(<EditMetadataDialog item={series()} open onOpenChange={vi.fn()} />);
    expect(screen.queryByText("Status")).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });

  it("unlocks every field before a reset refreshes from providers", async () => {
    mocks.updateAsync.mockResolvedValue({});
    const item = series([0, 1, 10]);
    render(<EditMetadataDialog item={item} open onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Reset to Provider" }));
    fireEvent.click(screen.getByText("Reset & Refresh"));

    await waitFor(() => expect(mocks.refresh).toHaveBeenCalledWith({ item, mode: "quick" }));
    expect(mocks.updateAsync).toHaveBeenCalledWith({ locked_fields: [] });
    expect(mocks.updateAsync.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.refresh.mock.invocationCallOrder[0]!,
    );
  });

  it("keeps the dialog open and does not refresh when unlocking fails", async () => {
    mocks.updateAsync.mockRejectedValue(new Error("nope"));
    const onOpenChange = vi.fn();
    render(<EditMetadataDialog item={series([0])} open onOpenChange={onOpenChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Reset to Provider" }));
    fireEvent.click(screen.getByText("Reset & Refresh"));

    await waitFor(() => expect(mocks.updateAsync).toHaveBeenCalled());
    // Let the rejected unlock settle before checking what the reset did next.
    await mocks.updateAsync.mock.results[0]!.value.catch(() => undefined);
    await Promise.resolve();
    expect(mocks.refresh).not.toHaveBeenCalled();
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
  });

  it("refreshes straight away when nothing is locked", () => {
    const item = series();
    render(<EditMetadataDialog item={item} open onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Reset to Provider" }));
    fireEvent.click(screen.getByText("Reset & Refresh"));

    expect(mocks.updateAsync).not.toHaveBeenCalled();
    expect(mocks.refresh).toHaveBeenCalledWith({ item, mode: "quick" });
  });
});
