import { MutationObserver, QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { ItemDetail } from "@/api/types";
import { catalogKeys } from "./keys";
import { updateCatalogItemDetail } from "./mediaSurfaceRefresh";
import { PERSONAL_STATE_WRITE_KEY } from "./personalStateWrites";

describe("updateCatalogItemDetail", () => {
  const settled = () => new Promise((resolve) => setTimeout(resolve, 0));
  const cacheInvalidatedDetail = async (queryClient: QueryClient, itemId: string) => {
    const key = catalogKeys.itemDetail(itemId);
    queryClient.setQueryData(key, { content_id: itemId, tagline: "old" });
    // Another device's metadata save, while no page shows the item.
    await queryClient.invalidateQueries({ queryKey: key, exact: true });
    return key;
  };
  const favorite = (detail: ItemDetail) => ({
    ...detail,
    user_state: { played: false, is_favorite: true, in_watchlist: false },
  });

  it("refetches an invalidated detail it patched once every pending write settles", async () => {
    const queryClient = new QueryClient();
    const invalidatedKey = await cacheInvalidatedDetail(queryClient, "item-1");
    const otherKey = catalogKeys.itemDetail("item-2");
    queryClient.setQueryData(otherKey, { content_id: "item-2", tagline: "old" });
    const start = (options: { mutationKey?: readonly unknown[]; onMutate?: () => void }) => {
      let land!: () => void;
      const done = new MutationObserver(queryClient, {
        ...options,
        mutationFn: () => new Promise<void>((resolve) => (land = resolve)),
      }).mutate();
      return async () => {
        land();
        await done;
        await settled();
      };
    };
    // Two overlapping toggles on item-1, as from its card and its page, one on
    // item-2, and an unrelated write that must not hold the refetch back.
    const toggle = (itemId: string) =>
      start({
        mutationKey: PERSONAL_STATE_WRITE_KEY,
        onMutate: () => updateCatalogItemDetail(queryClient, itemId, favorite),
      });
    const landFirst = toggle("item-1");
    const landSecond = toggle("item-1");
    const landOther = toggle("item-2");
    start({});
    await settled();

    // Nothing may read item-1 while a write to it is pending...
    expect(queryClient.getQueryData<ItemDetail>(invalidatedKey)?.user_state?.is_favorite).toBe(
      true,
    );
    expect(queryClient.getQueryState(invalidatedKey)?.isInvalidated).toBe(false);
    await landFirst();
    expect(queryClient.getQueryState(invalidatedKey)?.isInvalidated).toBe(false);

    // ...and once the last one lands, its detail refetches.
    await landSecond();
    await landOther();
    expect(queryClient.getQueryState(invalidatedKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(otherKey)?.isInvalidated).toBe(false);
  });

  it("refetches at once for a patch with no write behind it", async () => {
    const queryClient = new QueryClient();
    const key = await cacheInvalidatedDetail(queryClient, "item-1");

    // A realtime event from another device.
    updateCatalogItemDetail(queryClient, "item-1", favorite);
    await settled();

    expect(queryClient.getQueryData<ItemDetail>(key)?.user_state?.is_favorite).toBe(true);
    expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
  });
});
