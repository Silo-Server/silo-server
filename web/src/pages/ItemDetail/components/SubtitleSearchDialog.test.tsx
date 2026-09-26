import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { FileVersion } from "@/api/types";
import SubtitleSearchDialog from "./SubtitleSearchDialog";

const mocks = vi.hoisted(() => ({ providerStatus: vi.fn() }));
vi.mock("@/hooks/queries/subtitles", () => ({
  searchSubtitles: vi.fn(),
  detectSubtitleLanguage: vi.fn(),
  useDownloadSubtitle: () => ({ mutateAsync: vi.fn() }),
  useUploadSubtitle: () => ({ mutateAsync: vi.fn() }),
  useDownloadedSubtitles: () => ({ refetch: vi.fn() }),
  useSubtitleProviderStatus: mocks.providerStatus,
}));
vi.mock("@/components/subtitles/SubtitleUploadForm", () => ({
  SubtitleUploadForm: () => <div>Upload form</div>,
}));
vi.mock("./VersionFlyout", () => ({ buildQualitySummary: () => "" }));
afterEach(() => {
  cleanup();
  mocks.providerStatus.mockReset();
});

const version = { file_id: 42 } as FileVersion;

function renderDialog() {
  render(<SubtitleSearchDialog open onOpenChange={() => {}} version={version} title="Synthetic" />);
}

it("keeps upload but hides online search when the server reports no providers", () => {
  mocks.providerStatus.mockReturnValue({ data: { enabled: false, providers: [] } });
  renderDialog();
  expect(screen.getByText("Upload form")).toBeInTheDocument();
  expect(screen.queryByText("Search online")).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Search" })).not.toBeInTheDocument();
});

it.each([
  ["providers are enabled", { data: { enabled: true, providers: ["opensubtitles"] } }],
  ["the probe has not resolved or failed", { data: undefined }],
])("shows upload and online search when %s", (_case, status) => {
  mocks.providerStatus.mockReturnValue(status);
  renderDialog();
  expect(screen.getByText("Upload form")).toBeInTheDocument();
  expect(screen.getByText("Search online")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Search" })).toBeInTheDocument();
});

it("keeps focus inside the dialog when a late probe removes the focused search control", async () => {
  mocks.providerStatus.mockReturnValue({ data: undefined });
  const view = render(
    <SubtitleSearchDialog open onOpenChange={() => {}} version={version} title="Synthetic" />,
  );
  screen.getByRole("button", { name: "Search" }).focus();
  mocks.providerStatus.mockReturnValue({ data: { enabled: false, providers: [] } });
  view.rerender(
    <SubtitleSearchDialog open onOpenChange={() => {}} version={version} title="Synthetic" />,
  );
  expect(screen.queryByRole("button", { name: "Search" })).not.toBeInTheDocument();
  await waitFor(() =>
    expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true),
  );
});
