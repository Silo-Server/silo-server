import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StreamNode } from "@/api/types";
import type { AdminDownloadPreparationList } from "@/api/v2/adminDownloadPreparations";
import type {
  AdminDownloadEntry,
  AdminDownloadStorage,
  AdminDownloadStorageFile,
} from "@/api/v2/adminDownloadStorage";
import { makePreparation, makePreparationList } from "@/test/downloadPreparations";
import {
  makeDownloadDevice,
  makeStorage,
  makeStorageFile,
  makeStorageLocation,
} from "@/test/downloadStorage";

const mocks = vi.hoisted(() => ({
  storage: undefined as AdminDownloadStorage | undefined,
  files: [] as AdminDownloadStorageFile[],
  devicesQuery: [] as unknown[],
  deleteFiles: vi.fn(),
  cleanUp: vi.fn(),
  deleteUntracked: vi.fn(),
  revoke: vi.fn(),
  toastSuccess: vi.fn(),
  nodes: [] as StreamNode[],
  updateNode: vi.fn(),
  preparations: undefined as AdminDownloadPreparationList | undefined,
  entries: [] as AdminDownloadEntry[],
  toastError: vi.fn(),
  preparationAction: vi.fn(),
}));

function page<T>(items: T[]) {
  return {
    data: { pages: [{ items, page: { has_more: false } }] },
    isLoading: false,
    isError: false,
    hasNextPage: false,
    isFetchingNextPage: false,
    fetchNextPage: vi.fn(),
  };
}

vi.mock("@/hooks/queries/admin/downloadStorage", () => ({
  useAdminDownloadStorage: () => ({
    data: mocks.storage,
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
  }),
  useAdminDownloadStorageFiles: () => page(mocks.files),
  useAdminDownloadStorageEvents: () => page([]),
  useAdminDownloadDevices: (query: unknown) => {
    mocks.devicesQuery.push(query);
    return page([makeDownloadDevice()]);
  },
  useAdminDownloadDeviceEntries: () => page(mocks.entries),
  useDeleteAdminDownloadStorageFiles: () => ({ mutate: mocks.deleteFiles, isPending: false }),
  useCleanUpAdminDownloadStorageLocation: () => ({
    mutate: mocks.cleanUp,
    isPending: false,
    variables: undefined,
  }),
  useDeleteAdminDownloadStorageUntrackedFiles: () => ({
    mutate: mocks.deleteUntracked,
    isPending: false,
  }),
  useRevokeAdminDownloads: () => ({ mutate: mocks.revoke, isPending: false }),
}));
vi.mock("@/hooks/queries/admin/nodes", () => ({
  useAdminNodes: () => ({ data: mocks.nodes, isLoading: false }),
  useUpdateNode: () => ({ mutate: mocks.updateNode, isPending: false }),
}));
vi.mock("@/hooks/queries/admin/downloadPreparations", () => ({
  useAdminDownloadPreparations: () => ({
    data: mocks.preparations,
    isLoading: false,
    isError: false,
  }),
  useAdminDownloadPreparationAction: () => ({
    mutate: mocks.preparationAction,
    isPending: false,
    variables: undefined,
  }),
}));
vi.mock("@/hooks/queries/admin/logs", () => ({
  useOperationalLogs: () => ({ data: { entries: [] }, isLoading: false, isFetching: false }),
}));
vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, info: vi.fn(), error: mocks.toastError },
}));

import AdminDownloads from "./AdminDownloads";

function renderAt(path = "/admin/downloads") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AdminDownloads />
    </MemoryRouter>,
  );
}

const scratchNode = makeStorageLocation({
  key: "node:9",
  kind: "node",
  name: "node-gpu-1",
  node_id: "9",
  dir: "/transcode/download-artifacts",
  dir_source: "default",
  usage: {
    ...makeStorageLocation().usage!,
    fs_used_bytes: 842e9,
    fs_total_bytes: 1000e9,
    shares_scratch: true,
    bytes: 471e9,
  },
  in_use_bytes: 389e9,
  cached_bytes: 44e9,
  untracked_files: 6,
  untracked_bytes: 38e9,
});

beforeEach(() => {
  vi.clearAllMocks();
  mocks.storage = makeStorage({ locations: [makeStorageLocation(), scratchNode] });
  mocks.files = [];
  mocks.devicesQuery = [];
  mocks.nodes = [];
  mocks.preparations = undefined;
  mocks.entries = [];
});
afterEach(cleanup);

describe("AdminDownloads storage tab", () => {
  it("reports untracked files that could not be deleted", () => {
    mocks.deleteUntracked.mockImplementation(
      (
        _location: string,
        options: {
          onSuccess: (result: { files: number; bytes: number; failed_files: number }) => void;
        },
      ) => options.onSuccess({ files: 0, bytes: 0, failed_files: 2 }),
    );
    renderAt();
    const node = screen.getByRole("region", { name: "node-gpu-1" });
    fireEvent.click(within(node).getByRole("button", { name: "Review" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete untracked files" }));
    expect(mocks.toastError).toHaveBeenCalledWith(
      "2 untracked files on node-gpu-1 could not be deleted. Check the directory's permissions.",
    );
  });

  it("saves only the node override that changed", () => {
    mocks.nodes = [
      {
        id: 9,
        name: "node-gpu-1",
        config_etag: "1",
        download_artifact_max_bytes_override: 1.5e9,
      } as unknown as StreamNode,
    ];
    renderAt();
    const card = screen.getByRole("region", { name: "node-gpu-1" });
    fireEvent.click(within(card).getByRole("button", { name: "Edit location" }));
    expect(screen.getByLabelText("Budget in GB")).toHaveValue("1.5");
    fireEvent.change(screen.getByLabelText("Prepared file directory"), {
      target: { value: "/mnt/fast/silo" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.updateNode).toHaveBeenCalledWith(
      { node: mocks.nodes[0], body: { download_artifact_dir_override: "/mnt/fast/silo" } },
      expect.anything(),
    );
  });
});

describe("AdminDownloads prepared files tab", () => {
  it("deletes cached files and keeps in-use ones unless asked", () => {
    mocks.files = [
      makeStorageFile({ id: "art-dune" }),
      makeStorageFile({
        id: "art-opp",
        title: "Oppenheimer",
        state: "in_use",
        waiting: 1,
        finished: 0,
        bytes: 11.8e9,
      }),
    ];
    renderAt("/admin/downloads?tab=files");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select every listed file" }));
    fireEvent.click(screen.getByRole("button", { name: /Delete…/ }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText(/1 in use/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete 1 cached file" }));
    expect(mocks.deleteFiles).toHaveBeenCalledWith(
      { ids: ["art-dune"], includeInUse: false },
      expect.any(Object),
    );

    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Also delete the file in use/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete 2 files" }));
    expect(mocks.deleteFiles).toHaveBeenLastCalledWith(
      { ids: ["art-dune", "art-opp"], includeInUse: true },
      expect.any(Object),
    );
  });
});

describe("AdminDownloads prepared files selection", () => {
  it("clears the selection when the filters change", () => {
    mocks.files = [makeStorageFile({ id: "art-dune" })];
    renderAt("/admin/downloads?tab=files");
    const select = () => screen.getByRole("checkbox", { name: "Select every listed file" });
    fireEvent.click(select());
    expect(select()).toBeChecked();

    const search = screen.getByLabelText("Search titles");
    fireEvent.change(search, { target: { value: "dune" } });
    fireEvent.submit(search.closest("form")!);
    expect(select()).not.toBeChecked();
  });
});

describe("AdminDownloads device copies tab", () => {
  it("leaves a revoked download unchecked after the revoke", () => {
    const entry = {
      id: "dl-1",
      status: "completed",
      title: "Dune",
      effective_quality: "original",
    } as AdminDownloadEntry;
    mocks.entries = [entry];
    const view = renderAt("/admin/downloads?tab=devices");
    fireEvent.click(screen.getByRole("button", { name: "Show downloads on Pixel 8" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Dune" }));
    expect(screen.getByRole("checkbox", { name: "Select Dune" })).toBeChecked();

    mocks.entries = [{ ...entry, status: "revoked" }];
    view.rerender(
      <MemoryRouter initialEntries={["/admin/downloads?tab=devices"]}>
        <AdminDownloads />
      </MemoryRouter>,
    );
    expect(screen.getByRole("checkbox", { name: "Select Dune" })).not.toBeChecked();
  });

  it("opens with the stale filter from the storage banner and revokes a whole device", () => {
    renderAt("/admin/downloads?tab=devices&stale=1");
    expect(mocks.devicesQuery.at(-1)).toMatchObject({ stale: true });
    fireEvent.click(screen.getByRole("button", { name: /^Revoke all downloads on / }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText(/last seen/)).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText(/Reason/), { target: { value: "Lost phone" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke 12 downloads" }));
    expect(mocks.revoke).toHaveBeenCalledWith(
      {
        target: { userId: "7", profileId: "p-maya", deviceId: "dev-pixel", pauseMonitors: true },
        reason: "Lost phone",
      },
      expect.any(Object),
    );
  });
});

describe("AdminDownloads preparation tab", () => {
  it("acts on the selected jobs in bulk", () => {
    mocks.preparationAction.mockReset();
    mocks.preparations = makePreparationList([
      makePreparation(),
      makePreparation({
        id: "art-q",
        state: "queued",
        progress: undefined,
        media_title: "Queued One",
      }),
      makePreparation({
        id: "art-f",
        state: "failed",
        progress: undefined,
        media_title: "Failed One",
        requesters: [],
      }),
    ]);
    renderAt("/admin/downloads?tab=preparation");

    fireEvent.click(screen.getByRole("checkbox", { name: "Select all shown jobs" }));
    const toolbar = screen.getByRole("toolbar", { name: "Selected preparation jobs" });
    expect(within(toolbar).getByText("3 selected")).toBeInTheDocument();
    expect(within(toolbar).getByRole("button", { name: /Resume/ })).toBeDisabled();

    // A failed job cannot be paused, so it is left out of the pause.
    fireEvent.click(within(toolbar).getByRole("button", { name: /Pause 2/ }));
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "pause", ids: ["art-1", "art-q"] },
      expect.anything(),
    );

    fireEvent.click(within(toolbar).getByRole("button", { name: "Cancel 3" }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Cancel 3 jobs?")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel 3 jobs" }));
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "cancel", ids: ["art-1", "art-q", "art-f"] },
      expect.anything(),
    );

    fireEvent.click(within(toolbar).getByRole("button", { name: "Clear selection" }));
    expect(
      screen.queryByRole("toolbar", { name: "Selected preparation jobs" }),
    ).not.toBeInTheDocument();
  });

  it("acts only on selected jobs the filters show", () => {
    mocks.preparationAction.mockReset();
    mocks.preparations = makePreparationList([
      makePreparation(),
      makePreparation({
        id: "art-q",
        state: "queued",
        progress: undefined,
        media_title: "Queued One",
      }),
    ]);
    renderAt("/admin/downloads?tab=preparation");

    fireEvent.click(screen.getByRole("checkbox", { name: "Select all shown jobs" }));
    fireEvent.click(screen.getByRole("button", { name: /Queued\s*1/ }));
    const toolbar = screen.getByRole("toolbar", { name: "Selected preparation jobs" });
    expect(within(toolbar).getByText("1 selected")).toBeInTheDocument();
    fireEvent.click(within(toolbar).getByRole("button", { name: /Pause 1/ }));
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "pause", ids: ["art-q"] },
      expect.anything(),
    );
  });
});
