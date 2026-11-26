import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import HistoryImportSettings from "./HistoryImportSettings";

const idle = { isPending: false, error: null, mutateAsync: vi.fn() };
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: () => undefined }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p1", name: "Main", is_primary: true } }),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { role: "user" } }) }));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p1", name: "Main" }] }),
}));
vi.mock("@/hooks/queries/history-import", () => ({
  useHistoryImportSources: () => ({
    data: [
      { id: 1, name: "Living room Emby", source_type: "emby" },
      { id: 2, name: "Office Plex", source_type: "plex" },
    ],
    isLoading: false,
  }),
  useHistoryImportRuns: () => ({ data: [] }),
  useHistoryImportRun: () => ({ data: undefined, error: null, refetch: vi.fn() }),
  useHistoryImportCapability: () => ({ data: undefined }),
  useLoginEmbyConnect: () => idle,
  useCreateHistoryImportRun: () => idle,
}));

function show() {
  render(
    <MemoryRouter>
      <HistoryImportSettings />
    </MemoryRouter>,
  );
}

function expectNamedTextbox(name: string) {
  const field = screen.getByLabelText(name);
  expect(field.tagName).toBe("INPUT");
}

// Every field on the form takes its accessible name from its label (#5359).
describe("History Import form labels", () => {
  afterEach(cleanup);

  it("names the Emby Connect fields and the profile choice", () => {
    show();
    expectNamedTextbox("Email or Username");
    expectNamedTextbox("Password");
    expect(screen.getByRole("combobox", { name: "Import into profile" })).toBeTruthy();
  });

  it("names a saved Emby server's fields", () => {
    show();
    fireEvent.click(screen.getByRole("button", { name: "Saved Server" }));
    expect(screen.getByRole("combobox", { name: "Saved Server" })).toBeTruthy();
    expectNamedTextbox("Emby Username");
    expectNamedTextbox("Emby Password");
  });

  it("names a saved Plex server's fields", () => {
    show();
    fireEvent.click(screen.getByText("Plex", { exact: true }));
    fireEvent.click(screen.getByRole("button", { name: "Saved Server" }));
    expect(screen.getByRole("combobox", { name: "Saved Server" })).toBeTruthy();
    expectNamedTextbox("Plex Auth Token");
  });

  it("names the Jellyfin fields", () => {
    show();
    fireEvent.click(screen.getByText("Jellyfin", { exact: true }));
    expectNamedTextbox("Jellyfin Server URL");
    expectNamedTextbox("Jellyfin Username");
    expectNamedTextbox("Jellyfin Password");
  });
});
