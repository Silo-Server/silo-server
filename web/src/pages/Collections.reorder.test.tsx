import { act, render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { Collection } from "@/api/types";
import Collections from "./Collections";

const collection = (id: string, creator: string) =>
  ({
    id,
    name: id,
    creator_profile_id: creator,
    collection_type: "manual",
    is_shared: creator !== "me",
    group_id: null,
    last_sync_status: "",
    sync_schedule: "",
  }) as Collection;

const listed = vi.hoisted(() => [] as Collection[]);
const board = vi.hoisted(() => ({
  props: {} as {
    onBeginDrag?: (id: string) => void;
    onReorderInGroup?: (groupId: string | null, orderedIds: string[]) => void;
  },
}));
const mutate = vi.hoisted(() => vi.fn());
const errorToast = vi.hoisted(() => vi.fn());

vi.mock("@/components/collections/GroupedCollectionsBoard", () => ({
  GroupedCollectionsBoard: (props: typeof board.props) => {
    board.props = props;
    return null;
  },
  useGroupedCollectionCard: () => ({
    attributes: {},
    listeners: {},
    setNodeRef: () => {},
    transform: null,
    transition: undefined,
    isDragging: false,
  }),
}));
// The server's order read lists only this profile's own collections.
vi.mock("@/api/personalCollections", () => ({
  fetchCollectionOrderSnapshot: async () => ({
    ordered_ids: ["mine-1", "mine-2"],
    etag: '"order"',
  }),
  fetchCollectionEditSnapshot: async (id: string) => ({
    collection: listed.find((item) => item.id === id),
    etag: '"collection"',
  }),
  fetchGroupOrderSnapshot: vi.fn(),
  fetchGroupSnapshot: vi.fn(),
}));
vi.mock("sonner", () => ({ toast: { error: errorToast, success: vi.fn() } }));
vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: () => ({
    data: { imports: false, groups: true, artwork: false, item_reorder: true },
  }),
  useCollections: () => ({ data: listed, isLoading: false }),
  useCollectionGroups: () => ({ data: [] }),
  useServerCollections: () => ({ data: [] }),
  useCreateCollectionGroup: () => ({}),
  useDeleteCollection: () => ({}),
  useDeleteCollectionGroup: () => ({}),
  useReorderCollectionGroups: () => ({}),
  useReorderCollections: () => ({ mutate }),
  useUpdateCollection: () => ({}),
  useUpdateCollectionGroup: () => ({}),
}));
vi.mock("@/hooks/queries/userCollectionImports", () => ({ useSyncUserCollection: () => ({}) }));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "medium" } }),
}));
vi.mock("@/components/CollectionTemplateGallery", () => ({
  CollectionTemplateGallery: () => null,
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "me" } }),
}));

describe("reordering on the Collections page", () => {
  it("saves a drag with only the profile's own collections when a shared one is in the group", async () => {
    listed.splice(
      0,
      listed.length,
      collection("theirs", "them"),
      collection("mine-1", "me"),
      collection("mine-2", "me"),
    );
    render(
      <MemoryRouter>
        <Collections />
      </MemoryRouter>,
    );

    act(() => board.props.onBeginDrag?.("mine-2"));
    // The board reports the whole group, the shared collection included.
    act(() => board.props.onReorderInGroup?.(null, ["mine-2", "mine-1", "theirs"]));

    await waitFor(() =>
      expect(mutate).toHaveBeenCalledWith({
        orderedIds: ["mine-2", "mine-1"],
        groupId: null,
        etag: '"order"',
      }),
    );
    expect(errorToast).not.toHaveBeenCalled();
  });
});
