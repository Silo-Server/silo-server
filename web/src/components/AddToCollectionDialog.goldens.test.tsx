/**
 * Goldens: what the Add to collection dialog lists, what it reads and which
 * route an add takes. It lists only the profile's own manual collections, for
 * an acting admin too: the server collections group acting admins used to get
 * is gone (they add titles to a server collection from its editor).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { goldens } from "@/test/fixtures/collectionBodies";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import AddToCollectionDialog from "./AddToCollectionDialog";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
const account = vi.hoisted(() => ({ actingAdmin: false }));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => account.actingAdmin }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" } }),
}));
vi.mock("@/hooks/queries/libraries", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  )),
  useUserLibraries: () => ({ data: [{ id: 1, name: "Movies", type: "movies" }] }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

beforeEach(() => {
  account.actingAdmin = false;
});

function show() {
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
      }
    >
      <MemoryRouter>
        <AddToCollectionDialog
          open
          onOpenChange={vi.fn()}
          mediaItemId="movie:heat-1995"
          itemTitle="Heat"
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** The collections the dialog offers, by the name on each checkbox. */
function listedChoices() {
  return within(screen.getByRole("dialog"))
    .getAllByRole("checkbox")
    .map((box) => box.getAttribute("aria-label"));
}

describe("Add to collection", () => {
  it("lists only the profile's own manual collections for a regular profile", async () => {
    show();
    await screen.findByRole("checkbox", { name: "Rainy days" });
    expect(listedChoices()).toEqual(goldens.addToCollectionChoices);
    expect(v2Recorder.calls).toEqual(goldens.addToCollectionReads);
  });

  it("lists the same for an acting admin and reads no library's collections", async () => {
    account.actingAdmin = true;
    show();
    await screen.findByRole("checkbox", { name: "Rainy days" });
    expect(listedChoices()).toEqual(goldens.addToCollectionChoices);
    expect(v2Recorder.calls).toEqual(goldens.addToCollectionReads);
  });

  it("adds a title to a personal collection through the personal route", async () => {
    show();
    fireEvent.click(await screen.findByRole("checkbox", { name: "Rainy days" }));
    await waitFor(() => expect(v2Recorder.writes()).toEqual(goldens.addToPersonalCollection));
  });
});
