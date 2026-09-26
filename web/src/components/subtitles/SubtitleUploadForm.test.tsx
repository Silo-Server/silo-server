import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SubtitleUploadForm } from "./SubtitleUploadForm";

afterEach(cleanup);
it.each(["media", "unmount", "authority"])(
  "ignores a completed upload after %s changes",
  async (change) => {
    let finish!: () => void;
    const pending = new Promise<void>((resolve, reject) => {
      finish = () =>
        change === "authority"
          ? reject(new DOMException("Changed context", "AbortError"))
          : resolve();
    });
    const upload = vi.fn(() => pending),
      onSuccess = vi.fn(),
      onError = vi.fn();
    const props = { mediaFileId: 42, upload, onSuccess, onError, variant: "player" as const };
    const view = render(<SubtitleUploadForm {...props} />);
    const input = view.container.querySelector('input[type="file"]')!;
    fireEvent.change(input, { target: { files: [new File(["synthetic"], "synthetic.en.srt")] } });
    fireEvent.click(screen.getByRole("button", { name: "Upload" }));
    expect(upload).toHaveBeenCalledTimes(1);
    if (change === "media") view.rerender(<SubtitleUploadForm {...props} mediaFileId={43} />);
    if (change === "unmount") view.unmount();
    await act(async () => {
      finish();
      await pending.catch(() => {});
    });
    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  },
);
