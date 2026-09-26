import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { CollectionTemplate } from "@/lib/collectionTemplates";

import { UserCollectionTemplateConfigForm } from "./UserCollectionTemplateConfigForm";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}

const mocks = vi.hoisted(() => ({
  capabilities: {
    data: undefined as
      | {
          user_collection_sync_schedule: {
            custom_cron: boolean;
          };
        }
      | undefined,
    isPending: false,
    isError: true,
    isFetching: false,
    refetch: vi.fn(),
  },
  tmdbMutate: vi.fn(),
  traktMutate: vi.fn(),
  mdblistMutate: vi.fn(),
}));

vi.mock("@/hooks/queries/collections", () => ({
  useCollectionCapabilities: () => mocks.capabilities,
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => ({ data: [] }),
}));

vi.mock("@/hooks/queries/userCollectionImports", () => ({
  useImportUserTMDBCollection: () => ({ isPending: false, mutate: mocks.tmdbMutate }),
  useImportUserTraktCollection: () => ({ isPending: false, mutate: mocks.traktMutate }),
  useImportUserMDBListCollection: () => ({ isPending: false, mutate: mocks.mdblistMutate }),
}));

vi.mock("@/components/collections/CollectionDefaultSortField", () => ({
  CollectionDefaultSortField: () => null,
}));

vi.mock("@/pages/adminCollectionsShared", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/pages/adminCollectionsShared")>();
  return { ...actual, CollectionLibraryPicker: () => null };
});

vi.mock("./TemplatePosterField", () => ({
  TemplatePosterField: () => null,
}));

const template = {
  id: "tmdb_trending_movies_week",
  title: "Trending Movies This Week",
  description: "Top trending movies on TMDB.",
  icon: "🎬",
  category: "trending",
  source: "tmdb",
  media_kind: "movie",
  default_limit: 50,
  default_sync_schedule: "0 */6 * * *",
  tmdb: { preset: "trending", media_type: "movie", time_window: "week" },
} as CollectionTemplate;

describe("UserCollectionTemplateConfigForm", () => {
  beforeEach(() => {
    mocks.capabilities.data = undefined;
    mocks.capabilities.isPending = false;
    mocks.capabilities.isError = true;
    mocks.capabilities.isFetching = false;
    mocks.capabilities.refetch.mockReset();
    mocks.tmdbMutate.mockReset();
  });

  it("fails closed and offers a retry when schedule capabilities cannot be loaded", async () => {
    const user = userEvent.setup();
    render(
      <UserCollectionTemplateConfigForm
        template={template}
        onCancel={() => {}}
        onCreated={() => {}}
      />,
    );

    const submit = screen.getByRole("button", { name: "Create Collection" });
    expect(submit).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Collection creation stays unavailable until Silo confirms which sync schedules this account can use.",
    );

    fireEvent.submit(submit.closest("form")!);
    expect(mocks.tmdbMutate).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Retry schedule check" }));
    expect(mocks.capabilities.refetch).toHaveBeenCalledTimes(1);
  });

  it("submits the exact admin schedule after capabilities load", () => {
    mocks.capabilities.data = {
      user_collection_sync_schedule: { custom_cron: true },
    };
    mocks.capabilities.isError = false;

    render(
      <UserCollectionTemplateConfigForm
        template={template}
        onCancel={() => {}}
        onCreated={() => {}}
      />,
    );

    const submit = screen.getByRole("button", { name: "Create Collection" });
    expect(submit).toBeEnabled();
    fireEvent.submit(submit.closest("form")!);

    expect(mocks.tmdbMutate).toHaveBeenCalledWith(
      expect.objectContaining({ sync_schedule: "0 */6 * * *" }),
      expect.any(Object),
    );
  });
});
