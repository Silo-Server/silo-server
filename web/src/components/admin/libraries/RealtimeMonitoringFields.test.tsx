import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Library, LibraryRealtimeMonitoring } from "@/api/types";

import { RealtimeMonitoringFields } from "./LibraryFormSections";
import { useLibraryForm } from "./useLibraryForm";

const { monitoring, mutate } = vi.hoisted(() => ({
  monitoring: vi.fn(),
  mutate: vi.fn(),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useCreateLibrary: () => ({ mutate, isPending: false }),
  useUpdateLibrary: () => ({ mutate, isPending: false }),
  useSetLibraryProviders: () => ({ mutate: vi.fn(), isPending: false }),
  useLibraryProviders: () => ({ data: { levels: {} } }),
  useLibraryProviderDefaults: () => ({ data: { levels: {} }, isLoading: false }),
  useLibraryRealtimeMonitoring: () => monitoring(),
}));

const library = {
  id: 7,
  name: "Movies",
  type: "movies",
  paths: ["/media/movies"],
  enabled: true,
  realtime_monitoring: true,
} as Library;

function status(over: Partial<LibraryRealtimeMonitoring> = {}): {
  data: LibraryRealtimeMonitoring;
} {
  return {
    data: {
      server_enabled: true,
      libraries: [
        {
          library_id: 7,
          enabled: true,
          state: "monitoring",
          backend: "inotify",
          detail: "",
          directories: 4812,
          node_id: "node-a",
          updated_at: "2026-09-27T17:00:00Z",
        },
      ],
      ...over,
    },
  };
}

function Harness({ library }: { library: Library | null }) {
  const form = useLibraryForm({ library });
  return (
    <MemoryRouter>
      <RealtimeMonitoringFields form={form} />
      <button type="button" onClick={() => form.submit()}>
        Save
      </button>
    </MemoryRouter>
  );
}

const monitoringSwitch = () => screen.getByRole("switch", { name: "Real-time monitoring" });

describe("RealtimeMonitoringFields", () => {
  afterEach(cleanup);
  beforeEach(() => {
    mutate.mockClear();
    monitoring.mockReturnValue(status());
  });

  it("shows the state and the server's detail when monitoring is not working", () => {
    monitoring.mockReturnValue(
      status({
        libraries: [
          {
            library_id: 7,
            enabled: true,
            state: "limit_reached",
            backend: "inotify",
            detail: "Raise fs.inotify.max_user_watches on the host",
            directories: 90000,
          },
        ],
      }),
    );
    render(<Harness library={library} />);

    expect(
      screen.getByText(
        "Status: Watch limit reached · Raise fs.inotify.max_user_watches on the host",
      ),
    ).toBeTruthy();
  });

  it("disables the switch and links to the server setting when it is off server-wide", () => {
    monitoring.mockReturnValue(
      status({
        server_enabled: false,
        libraries: [
          {
            library_id: 7,
            enabled: true,
            state: "server_disabled",
            backend: "",
            detail: "",
            directories: 0,
          },
        ],
      }),
    );
    render(<Harness library={library} />);

    expect(monitoringSwitch().hasAttribute("disabled")).toBe(true);
    expect(screen.getByText(/turned off server-wide/)).toBeTruthy();
    const link = screen.getByRole("link", { name: "Settings → Library & Metadata" });
    expect(link.getAttribute("href")).toBe("/admin/settings/library");
    expect(screen.queryByText(/^Status:/)).toBeNull();
  });

  it("shows no status line for a new library", () => {
    render(<Harness library={null} />);

    expect(monitoringSwitch().getAttribute("aria-checked")).toBe("true");
    expect(screen.queryByText(/^Status:/)).toBeNull();
  });

  it("sends the switch with the update", async () => {
    render(<Harness library={library} />);

    expect(screen.getByText("Scan this library automatically when its files change.")).toBeTruthy();
    expect(screen.getByText("Status: Monitoring 4,812 folders (inotify)")).toBeTruthy();
    expect(monitoringSwitch().getAttribute("aria-checked")).toBe("true");
    expect(monitoringSwitch().hasAttribute("disabled")).toBe(false);

    await userEvent.click(monitoringSwitch());

    expect(monitoringSwitch().getAttribute("aria-checked")).toBe("false");
    expect(screen.queryByText(/^Status:/)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(mutate.mock.calls[0]![0]).toMatchObject({
      id: 7,
      body: { realtime_monitoring: false },
    });
  });
});
