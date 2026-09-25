import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";

import { getPerson } from "@/api/v2/people";
import { V2ProblemError } from "@/api/v2/request";
import { personKeys } from "@/hooks/queries/keys";
import PersonDetail from "./PersonDetail";

vi.mock("@/api/v2/people", () => ({
  getPerson: vi.fn(),
  refreshPerson: vi.fn(),
  adminRefreshPerson: vi.fn(),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: vi.fn(() => ({ user: null })) }));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: vi.fn(() => false) }));
vi.mock("@/hooks/queries/catalog", () => ({ useCatalogWindow: () => ({ isLoading: false }) }));
vi.mock("@/components/ItemGrid", () => ({ default: () => null }));

const clients: QueryClient[] = [];
afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
  vi.clearAllMocks();
});

function personProblem(status: number) {
  return new V2ProblemError("getPerson", {
    type: `https://silo.example/problems/${status === 404 ? "not_found" : "internal_error"}`,
    title: status === 404 ? "Not Found" : "Internal Server Error",
    status,
    detail: status === 404 ? "Person not found." : "People are unavailable.",
    instance: "/api/v2/catalog/people/7",
  });
}

function renderPerson(prefetched?: { id: string; name: string }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  if (prefetched) client.setQueryData(personKeys.detail(prefetched.id), prefetched);
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/person/7"]}>
        <Routes>
          <Route path="/person/:id" element={<PersonDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

it("gives a missing person a page with a way home and a Not found title", async () => {
  vi.mocked(getPerson).mockRejectedValue(personProblem(404));

  renderPerson();

  expect(
    await screen.findByRole("heading", { level: 1, name: "This person isn't available" }),
  ).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Go home" })).toHaveAttribute("href", "/");
  expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  expect(document.title).toContain("Not found");
});

it("offers a retry rather than calling a failed read missing", async () => {
  vi.mocked(getPerson).mockRejectedValueOnce(personProblem(500)).mockResolvedValue({
    id: "7",
    name: "Found Again",
  });

  renderPerson();

  await userEvent.click(await screen.findByRole("button", { name: "Try again" }));

  await waitFor(() =>
    expect(screen.getByRole("heading", { level: 1, name: "Found Again" })).toBeInTheDocument(),
  );
  expect(screen.queryByText("This person isn't available")).not.toBeInTheDocument();
});

it("replaces a prefetched person when the view read finds them gone", async () => {
  vi.mocked(getPerson).mockRejectedValue(personProblem(404));

  renderPerson({ id: "7", name: "Prefetched Actor" });

  expect(
    await screen.findByRole("heading", { level: 1, name: "This person isn't available" }),
  ).toBeInTheDocument();
  expect(screen.queryByText("Prefetched Actor")).not.toBeInTheDocument();
  expect(document.title).toContain("Not found");
});
