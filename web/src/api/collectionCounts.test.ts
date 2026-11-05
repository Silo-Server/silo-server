import { expect, it } from "vitest";
import adminCollection from "../../../contracts/api/v2/fixtures/get_admin_collection_ok.json";
import personalCollection from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import libraryTab from "../../../contracts/api/v2/fixtures/get_library_collections_ok.json";
import { adminCollectionFromV2 } from "./adminCollections";
import { collectionFromV2, serverCollectionsFromV2 } from "./personalCollections";
import { libraryCollectionFromV2, userCollectionFromV2 } from "./v2/catalog";
import { adminCollectionOptions } from "@/hooks/queries/homeRows/useRowCollectionOptions";
import { buildAllUserCollectionOptions } from "@/hooks/queries/useAllUserCollections";
import { PERSONAL_SCOPE, SERVER_SCOPE } from "@/lib/collections/scope";

it.each(["available", "unavailable", "future-status"])(
  "preserves %s through every collection adapter and option builder",
  (status) => {
    const counts = { item_count: 91, item_count_status: status };
    const personal = collectionFromV2({ ...personalCollection, ...counts, sync_cadence: "" });
    const admin = adminCollectionFromV2({ ...adminCollection, ...counts });
    const curated = libraryCollectionFromV2({ ...libraryTab.collections[0]!, ...counts });
    const user = userCollectionFromV2({ ...personalCollection, ...counts });
    expect(personal).toMatchObject(counts);
    expect(admin).toMatchObject(counts);
    expect(curated).toMatchObject(counts);
    expect(user).toMatchObject(counts);
    expect(PERSONAL_SCOPE.toView(personal).itemCount).toBe(status === "available" ? 91 : null);
    expect(SERVER_SCOPE.toView(admin).itemCount).toBe(status === "available" ? 91 : null);
    expect(adminCollectionOptions([curated], [{ id: 1, name: "Movies" }])[0]).toMatchObject(counts);
    const options = buildAllUserCollectionOptions(
      [{ id: 1, name: "Movies" }],
      [personal],
      [[curated]],
    );
    expect(options).toHaveLength(2);
    for (const option of options) expect(option).toMatchObject(counts);
    const libraries = serverCollectionsFromV2({
      libraries: [
        {
          library_id: "1",
          library_name: "Movies",
          total_count: 1,
          collections: [{ ...libraryTab.ungrouped.collections[0]!, ...counts }],
        },
      ],
    } as unknown as Parameters<typeof serverCollectionsFromV2>[0]);
    expect(libraries[0]?.collections[0]).toMatchObject(counts);
  },
);
