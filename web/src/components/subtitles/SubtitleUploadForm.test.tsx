import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SubtitleUploadForm } from "./SubtitleUploadForm";

afterEach(cleanup);

function chooseFile(container: HTMLElement, file: File) {
  const input = container.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error("file input not rendered");
  fireEvent.change(input, { target: { files: [file] } });
}

describe("SubtitleUploadForm", () => {
  it("drops the earlier selection when a later file is rejected", async () => {
    const upload = vi.fn(async () => {});
    const { container } = render(
      <SubtitleUploadForm mediaFileId={1} upload={upload} onSuccess={vi.fn()} />,
    );

    chooseFile(container, new File(["WEBVTT\n"], "c-valid.vtt", { type: "text/vtt" }));
    expect(screen.getByText(/c-valid\.vtt/)).toBeInTheDocument();

    chooseFile(container, new File(["notes"], "notes.txt", { type: "text/plain" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/Unsupported file type/);
    expect(screen.queryByText(/c-valid\.vtt/)).not.toBeInTheDocument();
    const uploadButton = screen.getByRole("button", { name: "Upload" });
    expect(uploadButton).toBeDisabled();
    fireEvent.click(uploadButton);
    expect(upload).not.toHaveBeenCalled();
  });
});
