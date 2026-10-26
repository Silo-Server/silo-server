import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { expect, it, vi } from "vitest";

import { setProfileToken } from "@/api/client";
import { ARR_POLL_PLUGIN, pollSource, stubAutoscanServer } from "@/test/autoscanServer";

import { SourceDialog } from "./SourceDialog";
import { setupAutoscanAdmin } from "./autoscanTestSession.test-support";

vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));

const source = pollSource({ label: "Old", enabled: false, connection_id: null });

setupAutoscanAdmin();

function mount() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3 }, queries: { retry: false } },
  });
  const onOpenChange = vi.fn();
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <SourceDialog
          mode="edit"
          open
          onOpenChange={onOpenChange}
          source={source}
          title="Old"
          description="Sonarr / Radarr"
          connectionOptions={[]}
          globalPollInterval={600}
          onRequestDelete={() => {}}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { onOpenChange };
}

it("saves the label edit as a full-state PUT under the opening profile", async () => {
  const { writes } = stubAutoscanServer({ plugins: [ARR_POLL_PLUGIN], sources: [source] });
  const { onOpenChange } = mount();
  const dialog = screen.getByRole("dialog", { name: "Edit source · Old" });
  fireEvent.mouseDown(within(dialog).getByRole("tab", { name: "General" }));
  const input = within(dialog).getByRole("textbox", { name: "Custom label (optional)" });
  fireEvent.change(input, { target: { value: "New" } });
  fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));

  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]!.path).toMatch(/\/api\/v2\/admin\/autoscan\/sources\/poll-a$/);
  expect(writes[0]!.method).toBe("PUT");
  expect(writes[0]!.body).toMatchObject({
    label: "New",
    enabled: false,
    delivery_mode: "poll",
    path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
    source_config: { lookback: "24h" },
  });
  await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
});

it("refuses to save a retained draft after the profile PIN is replaced", async () => {
  const { writes } = stubAutoscanServer({ plugins: [ARR_POLL_PLUGIN], sources: [source] });
  const { onOpenChange } = mount();
  const dialog = screen.getByRole("dialog");
  fireEvent.mouseDown(within(dialog).getByRole("tab", { name: "General" }));
  const input = within(dialog).getByRole("textbox", { name: "Custom label (optional)" });
  fireEvent.change(input, { target: { value: "Retained" } });

  act(() => setProfileToken("pin-b"));
  await act(async () => fireEvent.click(within(dialog).getByRole("button", { name: "Save" })));

  expect(writes).toHaveLength(0);
  expect(input).toHaveValue("Retained");
  expect(onOpenChange).not.toHaveBeenCalled();
});
