import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import listAudiobookGroupsOk from "../../../../contracts/api/v2/fixtures/list_audiobook_groups_ok.json";
import { V2TimeoutError } from "@/api/v2/request";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));

vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: mocks.v2,
}));

import AudiobookGroupsView from "./AudiobookGroupsView";

function renderView() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AudiobookGroupsView libraryId={4} groupBy="author" onSelectGroup={() => {}} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}

afterEach(() => {
  mocks.v2.mockReset();
});

describe("AudiobookGroupsView load errors", () => {
  it("shows the load error instead of an empty list and retries", async () => {
    mocks.v2
      .mockRejectedValueOnce(new V2TimeoutError("listAudiobookGroups", 30_000))
      .mockResolvedValue(listAudiobookGroupsOk);
    renderView();

    expect(
      await screen.findByRole("heading", { name: "Couldn't load these authors" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("The server isn't responding. Try again in a moment."),
    ).toBeInTheDocument();
    expect(screen.queryByText("No authors found in this library.")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByText("Frank Herbert")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load these authors")).not.toBeInTheDocument();
  });

  it("keeps loaded groups when a later refetch fails", async () => {
    mocks.v2
      .mockResolvedValueOnce(listAudiobookGroupsOk)
      .mockRejectedValue(new TypeError("Failed to fetch"));
    const client = renderView();
    expect(await screen.findByText("Frank Herbert")).toBeInTheDocument();

    await act(() => client.refetchQueries());

    expect(screen.getByText("Frank Herbert")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load these authors")).not.toBeInTheDocument();
  });

  it("still says the library has none when the read succeeds empty", async () => {
    mocks.v2.mockResolvedValue({ ...listAudiobookGroupsOk, items: [], total: 0, page: {} });
    renderView();

    expect(await screen.findByText("No authors found in this library.")).toBeInTheDocument();
  });
});
