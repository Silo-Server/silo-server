import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SubtitleUploadForm, type SubtitleUploadInput } from "./SubtitleUploadForm";

afterEach(cleanup);

function chooseFile(container: HTMLElement, file: File) {
  const input = container.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error("file input not rendered");
  fireEvent.change(input, { target: { files: [file] } });
}

describe("SubtitleUploadForm", () => {
  it("drops the earlier selection when a later file is rejected", async () => {
    const upload = vi.fn(async () => {});
    const detectLanguage = vi.fn(async () => ({ language: "fr", source: "content" as const }));
    const { container } = render(
      <SubtitleUploadForm
        mediaFileId={1}
        upload={upload}
        detectLanguage={detectLanguage}
        onSuccess={vi.fn()}
        variant="player"
      />,
    );
    const languageSelect = screen.getByRole("combobox", { name: "Upload language" });

    chooseFile(container, new File(["WEBVTT\n"], "c-valid.vtt", { type: "text/vtt" }));
    expect(screen.getByText(/c-valid\.vtt/)).toBeInTheDocument();
    await waitFor(() => expect(languageSelect).toHaveValue("fr"));

    chooseFile(container, new File(["notes"], "notes.txt", { type: "text/plain" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/Unsupported file type/);
    expect(screen.queryByText(/c-valid\.vtt/)).not.toBeInTheDocument();
    expect(languageSelect).toHaveValue("en");
    const uploadButton = screen.getByRole("button", { name: "Upload" });
    expect(uploadButton).toBeDisabled();
    fireEvent.click(uploadButton);
    expect(upload).not.toHaveBeenCalled();
  });

  it("keeps a language the user picked when a file is rejected", async () => {
    // With no language cues in the file, the detector echoes the user's pick
    // back as a "manual" fallback.
    const detectLanguage = vi.fn(async (_file: File, fallback?: string) => ({
      language: fallback ?? "",
      source: "manual" as const,
    }));
    const { container } = render(
      <SubtitleUploadForm
        mediaFileId={1}
        upload={vi.fn(async () => {})}
        detectLanguage={detectLanguage}
        onSuccess={vi.fn()}
        variant="player"
      />,
    );
    const languageSelect = screen.getByRole("combobox", { name: "Upload language" });
    fireEvent.change(languageSelect, { target: { value: "de" } });
    chooseFile(container, new File(["1\n"], "episode.srt", { type: "application/x-subrip" }));
    await waitFor(() => expect(detectLanguage).toHaveBeenCalledWith(expect.any(File), "de"));
    await waitFor(() => expect(languageSelect).not.toBeDisabled());

    chooseFile(container, new File(["notes"], "notes.txt", { type: "text/plain" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/Unsupported file type/);
    expect(languageSelect).toHaveValue("de");
  });

  it("keeps the uploading file selected when another file is chosen mid-upload", async () => {
    let finishUpload: () => void = () => {};
    const upload = vi.fn(
      (_input: SubtitleUploadInput) =>
        new Promise<void>((resolve) => {
          finishUpload = resolve;
        }),
    );
    const onSuccess = vi.fn();
    const { container } = render(
      <SubtitleUploadForm mediaFileId={1} upload={upload} onSuccess={onSuccess} variant="player" />,
    );
    chooseFile(container, new File(["WEBVTT\n"], "a.vtt", { type: "text/vtt" }));
    fireEvent.click(screen.getByRole("button", { name: "Upload" }));
    await screen.findByRole("button", { name: "Uploading…" });

    chooseFile(container, new File(["1\n"], "b.srt", { type: "application/x-subrip" }));
    fireEvent.drop(screen.getByRole("button", { name: "Drop subtitle file here or browse" }), {
      dataTransfer: { files: [new File(["notes"], "notes.txt", { type: "text/plain" })] },
    });

    expect(screen.getByText(/a\.vtt/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(upload).toHaveBeenCalledTimes(1);
    expect(upload.mock.calls[0]?.[0].file.name).toBe("a.vtt");

    await act(async () => finishUpload());
    expect(onSuccess).toHaveBeenCalledTimes(1);
  });

  it("ignores a detection result that arrives after the file is rejected", async () => {
    let finishDetection: (result: { language: string; source: "content" }) => void = () => {};
    const detectLanguage = vi.fn(
      () =>
        new Promise<{ language: string; source: "content" }>((resolve) => {
          finishDetection = resolve;
        }),
    );
    const { container } = render(
      <SubtitleUploadForm
        mediaFileId={1}
        upload={vi.fn(async () => {})}
        detectLanguage={detectLanguage}
        onSuccess={vi.fn()}
        variant="player"
      />,
    );
    const languageSelect = screen.getByRole("combobox", { name: "Upload language" });

    chooseFile(container, new File(["WEBVTT\n"], "c-valid.vtt", { type: "text/vtt" }));
    expect(languageSelect).toBeDisabled();
    chooseFile(container, new File(["notes"], "notes.txt", { type: "text/plain" }));
    expect(languageSelect).not.toBeDisabled();

    await act(async () => finishDetection({ language: "fr", source: "content" }));

    expect(languageSelect).toHaveValue("en");
    expect(screen.queryByText(/Detected/)).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(/Unsupported file type/);
    expect(screen.getByRole("button", { name: "Upload" })).toBeDisabled();
  });
});
