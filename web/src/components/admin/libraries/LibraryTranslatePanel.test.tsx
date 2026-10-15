import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  v2: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
  status: { enabled: true, on_view: "off" },
}));

vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: mocks.v2 };
});

vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

vi.mock("@/hooks/queries/metadataAI", () => ({
  useMetadataAIStatus: () => ({ data: mocks.status }),
}));

import { catalogKeys } from "@/hooks/queries/keys";

import { LibraryTranslatePanel } from "./LibraryTranslatePanel";

function renderPanel(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return render(<LibraryTranslatePanel libraryId={4} />, { wrapper });
}

function job(overrides: object) {
  return {
    id: "11",
    target_kind: "library",
    content_id: "library:4",
    include_children: true,
    source_language: "",
    target_language: "de",
    engine: "openai",
    model: "m",
    status: "running",
    progress: 0.5,
    progress_message: "Translating item 2 of 4",
    fields_done: 1,
    fields_total: 4,
    force: false,
    created_at: "2026-10-09T00:00:00.000Z",
    updated_at: "2026-10-09T00:00:00.000Z",
    ...overrides,
  };
}

describe("LibraryTranslatePanel", () => {
  afterEach(() => {
    mocks.v2.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();
    mocks.status = { enabled: true, on_view: "off" };
  });

  it("starts a prewarm for the chosen language", async () => {
    mocks.v2.mockImplementation((route: string) =>
      route.startsWith("GET")
        ? Promise.resolve({ jobs: [] })
        : Promise.resolve(job({ status: "pending" })),
    );
    renderPanel();
    await userEvent.selectOptions(await screen.findByLabelText("Language"), "de");
    await userEvent.click(screen.getByRole("button", { name: "Translate" }));
    await waitFor(() =>
      expect(mocks.v2).toHaveBeenCalledWith(
        "POST /api/v2/admin/libraries/{library_id}/metadata-translation",
        expect.objectContaining({ path: { library_id: "4" }, body: { target_language: "de" } }),
      ),
    );
  });

  it("shows progress and stops a running prewarm", async () => {
    mocks.v2.mockImplementation((route: string) =>
      route.startsWith("GET") ? Promise.resolve({ jobs: [job({})] }) : Promise.resolve(undefined),
    );
    renderPanel();
    expect(await screen.findByText("Translating item 2 of 4 (50%)")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() =>
      expect(mocks.v2).toHaveBeenCalledWith(
        "POST /api/v2/admin/libraries/{library_id}/metadata-translation/jobs/{job_id}/cancel",
        expect.objectContaining({ path: { library_id: "4", job_id: "11" } }),
      ),
    );
  });

  it("is hidden while AI description translation is off", () => {
    mocks.status = { enabled: false, on_view: "off" };
    const { container } = renderPanel();
    expect(container).toBeEmptyDOMElement();
    expect(mocks.v2).not.toHaveBeenCalled();
  });

  it("reports a prewarm that finishes before the first poll", async () => {
    let jobs: object[] = [];
    mocks.v2.mockImplementation((route: string) => {
      if (route.startsWith("GET")) return Promise.resolve({ jobs });
      // The job is already finished by the time the list is read again.
      jobs = [
        job({
          status: "completed",
          progress: 1,
          progress_message: "Translated 2 descriptions across 1 items",
        }),
      ];
      return Promise.resolve(job({ status: "pending" }));
    });
    renderPanel();
    await userEvent.selectOptions(await screen.findByLabelText("Language"), "de");
    await userEvent.click(screen.getByRole("button", { name: "Translate" }));
    await waitFor(() =>
      expect(mocks.toastSuccess).toHaveBeenCalledWith("Translated 2 descriptions across 1 items"),
    );
  });

  it("reports each of two prewarms when the newer one finishes first", async () => {
    let jobs = [job({ id: "12", target_language: "fr" }), job({ id: "11" })];
    mocks.v2.mockImplementation(() => Promise.resolve({ jobs }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderPanel(client);
    expect(await screen.findAllByRole("status")).toHaveLength(2);

    jobs = [
      job({
        id: "12",
        target_language: "fr",
        status: "completed",
        progress_message: "French done",
      }),
      job({ id: "11" }),
    ];
    await client.refetchQueries();
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith("French done"));

    jobs = [
      job({
        id: "12",
        target_language: "fr",
        status: "completed",
        progress_message: "French done",
      }),
      job({ id: "11", status: "completed", progress_message: "German done" }),
    ];
    await client.refetchQueries();
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith("German done"));
    expect(mocks.toastSuccess).toHaveBeenCalledTimes(2);
  });

  it("refreshes localized media views after a prewarm that failed partway", async () => {
    let jobs = [job({})];
    mocks.v2.mockImplementation(() => Promise.resolve({ jobs }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const catalogKey = [...catalogKeys.all, "list"];
    const favoritesKey = ["favorites", "list"];
    client.setQueryData(catalogKey, { items: [] });
    client.setQueryData(favoritesKey, { items: [] });
    renderPanel(client);
    expect(await screen.findByRole("status")).toBeInTheDocument();

    jobs = [job({ status: "failed", error_message: "quota exceeded" })];
    await client.refetchQueries({ queryKey: ["admin", "library-metadata-translation"] });
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledWith("quota exceeded"));
    await waitFor(() => expect(client.getQueryState(catalogKey)?.isInvalidated).toBe(true));
    expect(client.getQueryState(favoritesKey)?.isInvalidated).toBe(true);
  });

  it("reports a prewarm that finished while the editor was closed", async () => {
    let jobs = [job({})];
    mocks.v2.mockImplementation(() => Promise.resolve({ jobs }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const first = renderPanel(client);
    expect(await screen.findByRole("status")).toBeInTheDocument();
    first.unmount();

    // Long enough for the unused jobs query to be garbage-collected.
    client.removeQueries();
    jobs = [job({ status: "completed", progress_message: "German done" })];
    renderPanel(client);
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith("German done"));
  });

  it("reports a prewarm whose start answered after the editor closed", async () => {
    let jobs: object[] = [];
    let answer: (value: object) => void = () => {};
    mocks.v2.mockImplementation((route: string) =>
      route.startsWith("GET")
        ? Promise.resolve({ jobs })
        : new Promise((resolve) => {
            answer = resolve;
          }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const first = renderPanel(client);
    await userEvent.selectOptions(await screen.findByLabelText("Language"), "de");
    await userEvent.click(screen.getByRole("button", { name: "Translate" }));
    first.unmount();
    answer(job({ status: "pending" }));
    await waitFor(() => expect(client.isMutating()).toBe(0));

    client.removeQueries();
    jobs = [job({ status: "completed", progress_message: "German done" })];
    renderPanel(client);
    await waitFor(() => expect(mocks.toastSuccess).toHaveBeenCalledWith("German done"));
  });

  it("offers a Stop for each of two running prewarms", async () => {
    mocks.v2.mockImplementation((route: string) =>
      route.startsWith("GET")
        ? Promise.resolve({ jobs: [job({ id: "12", target_language: "fr" }), job({ id: "11" })] })
        : Promise.resolve(undefined),
    );
    renderPanel();
    const stops = await screen.findAllByRole("button", { name: "Stop" });
    expect(stops).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Translate" })).toBeDisabled();
    await userEvent.click(stops[1]!);
    await waitFor(() =>
      expect(mocks.v2).toHaveBeenCalledWith(
        "POST /api/v2/admin/libraries/{library_id}/metadata-translation/jobs/{job_id}/cancel",
        expect.objectContaining({ path: { library_id: "4", job_id: "11" } }),
      ),
    );
  });
});
