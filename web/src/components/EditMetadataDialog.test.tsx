import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  update: vi.fn(),
  refresh: vi.fn(),
}));

vi.mock("@/hooks/queries/items", () => ({
  useUpdateItemMetadata: () => ({ mutate: mocks.update, isPending: false }),
  useRefreshItemMetadata: () => ({ mutate: mocks.refresh }),
}));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => true }));
vi.mock("@/components/MetadataTranslatePanel", () => ({ MetadataTranslatePanel: () => null }));
vi.mock("@/components/ConfirmDialog", () => ({ ConfirmDialog: () => null }));
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
