// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { ApiClientsWidget } from "./ApiClientsWidget";

const state = vi.hoisted(() => ({ pending: false }));
const client = {
  session_id: "s1",
  user_id: "2",
  username: "kim",
  client_name: "Silo",
  device_name: "Kim's iPhone",
  device_platform: "ios",
  user_agent: "",
  requests: 10,
  refused: 0,
  peak_hour: "2026-11-26T09:00:00Z",
  peak_hour_requests: 10,
  first_hour: "2026-11-26T09:00:00Z",
  last_hour: "2026-11-26T10:00:00Z",
  session_active: true,
};
vi.mock("@/hooks/queries/admin/apiClients", () => ({
  MAX_ENDPOINT_HOURS: 24,
  useAdminApiClients: () => ({
    isLoading: false,
    error: null,
    data: { clients: [client], total_requests: 10 },
  }),
  useAdminApiClientEndpoints: () => ({ isLoading: false, error: null, data: { endpoints: [] } }),
  useRevokeApiClientSession: () => ({
    isPending: state.pending,
    error: null,
    mutate: vi.fn(),
    reset: vi.fn(),
  }),
}));
// The Silo app sentence asks the server whether app keys are on; a marker
// stands in for it, showing whether it carries its link.
vi.mock("@/components/appKeys/SignsBackInNote", () => ({
  SignsBackInNote: ({ to }: { to?: unknown }) => (
    <span>{to === undefined ? "Silo app sentence without link" : "Silo app sentence"}</span>
  ),
}));

afterEach(() => {
  cleanup();
  state.pending = false;
});

// While the sign-out runs its dialog must stay open for the result: the
// sentence's link, which would close it and leave the page, becomes words.
it("drops the sentence's link while the sign-out runs", () => {
  const view = render(
    <MemoryRouter>
      <ApiClientsWidget />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getAllByRole("button", { expanded: false })[0]!);
  fireEvent.click(screen.getByRole("button", { name: "Sign out this session" }));
  expect(within(screen.getByRole("alertdialog")).getByText("Silo app sentence")).toBeTruthy();
  state.pending = true;
  view.rerender(
    <MemoryRouter>
      <ApiClientsWidget />
    </MemoryRouter>,
  );
  expect(
    within(screen.getByRole("alertdialog")).getByText("Silo app sentence without link"),
  ).toBeTruthy();
});
