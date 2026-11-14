import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Link, MemoryRouter, Route, Routes, useLocation } from "react-router";
import { V2ProblemError } from "@/api/v2/request";
import LibraryItemLink from "./LibraryItemLink";

const mocks = vi.hoisted(() => ({ fetchDetail: vi.fn(), toastError: vi.fn() }));

vi.mock("@/hooks/queries/catalogRead", () => ({ fetchCatalogItemDetail: mocks.fetchDetail }));
vi.mock("sonner", () => ({ toast: { error: mocks.toastError } }));

function problem(status: number) {
  return new V2ProblemError("getCatalogItem", {
    type: "https://silo.example/problems/test",
    title: "Problem",
    status,
    detail: "Problem.",
    instance: "/api/v2/catalog/items/movie-603",
  });
}

function Location() {
  return <output data-testid="location">{useLocation().pathname}</output>;
}

function renderLink() {
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={["/requests"]}>
        <Routes>
          <Route
            path="/requests"
            element={
              <>
                <LibraryItemLink contentId="movie-603">Open</LibraryItemLink>
                <Link to="/elsewhere">Leave</Link>
              </>
            }
          />
          <Route path="*" element={null} />
        </Routes>
        <Location />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  const link = screen.getByRole("link", { name: "Open" });
  expect(link).toHaveAttribute("href", "/item/movie-603");
  fireEvent.click(link);
}

describe("LibraryItemLink", () => {
  beforeEach(() => {
    mocks.fetchDetail.mockReset();
    mocks.toastError.mockReset();
  });

  it.each([
    ["the viewer can read the item", () => Promise.resolve({ content_id: "movie-603" })],
    ["the check fails for another reason", () => Promise.reject(problem(500))],
  ])("opens the item page when %s", async (_, result) => {
    mocks.fetchDetail.mockImplementation(result);
    renderLink();

    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/item/movie-603"),
    );
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("drops a check the viewer abandoned by leaving the page", async () => {
    let resolve!: (value: unknown) => void;
    mocks.fetchDetail.mockReturnValue(new Promise((r) => (resolve = r)));
    renderLink();
    fireEvent.click(screen.getByRole("link", { name: "Leave" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/elsewhere");

    resolve({ content_id: "movie-603" });
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.getByTestId("location")).toHaveTextContent("/elsewhere");
  });

  it("stays put and says why when the viewer cannot open the item", async () => {
    mocks.fetchDetail.mockRejectedValue(problem(404));
    renderLink();

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledOnce());
    expect(screen.getByTestId("location")).toHaveTextContent("/requests");
  });
});
