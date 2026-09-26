import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { StaleApiRequestContextError } from "@/api/client";
import type { FileVersion } from "@/api/types";
import DownloadVersionPicker from "./DownloadVersionPicker";
const mocks = vi.hoisted(() => ({ launch: vi.fn(), error: vi.fn() }));
vi.mock("@/api/v2/directDownloads", () => ({ launchDirectDownload: mocks.launch }));
vi.mock("sonner", () => ({ toast: { error: mocks.error } }));
const versions = [{ file_id: 42, resolution: "1080p", file_size: 1024 } as FileVersion];
beforeEach(() => {
  vi.clearAllMocks();
});
it("launches the selected file once and closes after navigation dispatch", async () => {
  mocks.launch.mockResolvedValue(undefined);
  const close = vi.fn();
  render(<DownloadVersionPicker open onOpenChange={close} versions={versions} />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: /1080p/ }));
  });
  expect(mocks.launch).toHaveBeenCalledTimes(1);
  expect(mocks.launch).toHaveBeenCalledWith(42, expect.any(Function));
  expect(close).toHaveBeenCalledWith(false);
});
it("retires a pending selection on replacement and rejects duplicate clicks", async () => {
  let resolve!: () => void;
  mocks.launch.mockImplementation(
    () =>
      new Promise<void>((r) => {
        resolve = r;
      }),
  );
  const close = vi.fn();
  const view = render(<DownloadVersionPicker open onOpenChange={close} versions={versions} />);
  fireEvent.click(screen.getByRole("button", { name: /1080p/ }));
  fireEvent.click(screen.getByRole("button", { name: /1080p/ }));
  expect(mocks.launch).toHaveBeenCalledTimes(1);
  const call = mocks.launch.mock.calls[0];
  if (!call) throw new Error("Expected one launch call");
  const current = call[1] as () => boolean;
  view.rerender(
    <DownloadVersionPicker
      open
      onOpenChange={close}
      versions={[{ ...versions[0], file_id: 43 } as FileVersion]}
    />,
  );
  expect(current()).toBe(false);
  await act(async () => resolve());
  expect(close).not.toHaveBeenCalled();
  expect(mocks.error).not.toHaveBeenCalled();
});

it("keeps an authority refusal from closing or reporting into the replacement profile", async () => {
  mocks.launch.mockRejectedValue(new StaleApiRequestContextError());
  const close = vi.fn();
  render(<DownloadVersionPicker open onOpenChange={close} versions={versions} />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: /1080p/ }));
  });
  expect(close).not.toHaveBeenCalled();
  expect(mocks.error).not.toHaveBeenCalled();
});
