import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SubtitleMenu } from "./SubtitleMenu";
import { SubtitleSearchModal } from "./SubtitleSearchModal";
import type { PlayerConfig } from "../context/PlayerConfigContext";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));
vi.mock("../player-v2", () => ({ playerV2: mocks.v2 }));
vi.mock("@/components/subtitles/SubtitleUploadForm", () => ({
  SubtitleUploadForm: () => <div>Upload form</div>,
}));
afterEach(() => {
  cleanup();
  mocks.v2.mockReset();
});

const config: PlayerConfig = {
  apiBaseUrl: "/api/v1",
  getAccessToken: () => "synthetic",
  getProfileId: () => "profile-1",
  getDeviceId: () => "synthetic-device",
};

function mockProviderStatus(status: Promise<unknown>) {
  // The menu consumes the rejection itself; mark it handled for the runner.
  status.catch(() => {});
  mocks.v2.mockImplementation((_config: PlayerConfig, route: string) =>
    route === "GET /api/v2/subtitles/providers/status" ? status : Promise.resolve({}),
  );
}

async function openAddSubtitles() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <SubtitleMenu
        tracks={[]}
        activeIndex={null}
        onSelect={() => {}}
        delayMs={0}
        onDelayChange={() => {}}
        mediaFileId={42}
        playerConfig={config}
      />
    </QueryClientProvider>,
  );
  await waitFor(() =>
    expect(mocks.v2).toHaveBeenCalledWith(config, "GET /api/v2/subtitles/providers/status", {}),
  );
  fireEvent.click(screen.getByRole("button", { name: "Enable captions" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "Add Subtitles…" }));
  return screen.getByRole("dialog", { name: "Add Subtitles" });
}

it("keeps upload but hides online search when the server reports no providers", async () => {
  mockProviderStatus(Promise.resolve({ enabled: false, providers: [] }));
  await openAddSubtitles();
  expect(screen.getByText("Upload form")).toBeInTheDocument();
  await waitFor(() => expect(screen.queryByText("Search online")).not.toBeInTheDocument());
  expect(screen.queryByRole("button", { name: "Search" })).not.toBeInTheDocument();
  await waitFor(() => expect(screen.getByRole("button", { name: "Close" })).toHaveFocus());
});

it.each([
  ["providers are enabled", () => Promise.resolve({ enabled: true, providers: ["opensubtitles"] })],
  ["the server predates the probe", () => Promise.resolve({})],
  ["the probe fails", () => Promise.reject(new Error("unavailable"))],
])("shows upload and online search when %s", async (_case, status) => {
  mockProviderStatus(status());
  await openAddSubtitles();
  expect(screen.getByText("Upload form")).toBeInTheDocument();
  expect(screen.getByText("Search online")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Search" })).toBeInTheDocument();
});

it("keeps focus in the modal when a late probe hides online search", async () => {
  const props = {
    mediaFileId: 42,
    playerConfig: config,
    isOpen: true,
    onClose: () => {},
    onSubtitleDownloaded: () => {},
  };
  const view = render(<SubtitleSearchModal {...props} onlineSearchEnabled />);
  await waitFor(() => expect(screen.getByRole("combobox", { name: "Language" })).toHaveFocus());
  view.rerender(<SubtitleSearchModal {...props} onlineSearchEnabled={false} />);
  await waitFor(() => expect(screen.getByRole("button", { name: "Close" })).toHaveFocus());
});

it("does not carry a disabled answer over to a new server", async () => {
  const serverB: PlayerConfig = { ...config, apiBaseUrl: "/server-b/api/v1" };
  mocks.v2.mockImplementation((cfg: PlayerConfig, route: string) => {
    if (route !== "GET /api/v2/subtitles/providers/status") return Promise.resolve({});
    // Server A reports no providers; server B's probe never settles.
    return cfg === config ? Promise.resolve({ enabled: false }) : new Promise(() => {});
  });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const menu = (playerConfig: PlayerConfig) => (
    <QueryClientProvider client={queryClient}>
      <SubtitleMenu
        tracks={[]}
        activeIndex={null}
        onSelect={() => {}}
        delayMs={0}
        onDelayChange={() => {}}
        mediaFileId={42}
        playerConfig={playerConfig}
      />
    </QueryClientProvider>
  );
  const view = render(menu(config));
  fireEvent.click(screen.getByRole("button", { name: "Enable captions" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "Add Subtitles…" }));
  await waitFor(() => expect(screen.queryByText("Search online")).not.toBeInTheDocument());
  view.rerender(menu(serverB));
  await waitFor(() => expect(screen.getByText("Search online")).toBeInTheDocument());
});
