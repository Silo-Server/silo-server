// @vitest-environment jsdom

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { FileVersion, VersionAudioTrack } from "@/api/types";
import AudioTracksPopover from "./AudioTracksPopover";

function versionWith(audioTracks: VersionAudioTrack[]): FileVersion {
  return { file_id: 1, audio_tracks: audioTracks } as FileVersion;
}

function openAudioMenu(version: FileVersion) {
  const view = render(
    <AudioTracksPopover version={version} onSelectTrack={() => {}} onResetSelection={() => {}} />,
  );
  fireEvent.click(screen.getByRole("button", { name: /^Audio/ }));
  return view;
}

describe("AudioTracksPopover", () => {
  it("identifies an unlabelled row by codec and channel layout instead of an unknown language", () => {
    openAudioMenu(versionWith([{ codec: "eac3", channels: 6 }]));

    expect(screen.getByText("EAC3 5.1")).toBeInTheDocument();
    expect(screen.queryByText("Unknown")).toBeNull();
    expect(screen.queryByText("Inconnue")).toBeNull();
    expect(screen.getByTitle("Language: Unknown")).toBeInTheDocument();
  });

  it("keeps the language name for a row that declares one", () => {
    openAudioMenu(versionWith([{ language: "fr", codec: "aac", channels: 2 }]));

    expect(screen.getByText("French")).toBeInTheDocument();
    expect(screen.queryByText("Language: Unknown")).toBeNull();
  });

  it("falls back to the row's position when neither language nor format is known", () => {
    openAudioMenu(versionWith([{ language: "en", codec: "eac3", channels: 6 }, {}]));

    expect(screen.getByText("English")).toBeInTheDocument();
    expect(screen.getByText("Track 2")).toBeInTheDocument();
    expect(screen.queryByText("Unknown")).toBeNull();
  });

  it("relabels the row when a probe replaces the unlabelled release entry", () => {
    const view = openAudioMenu(versionWith([{ codec: "eac3", channels: 6 }]));
    expect(screen.getByText("EAC3 5.1")).toBeInTheDocument();

    view.rerender(
      <AudioTracksPopover
        version={versionWith([{ language: "fr", codec: "eac3", channels: 6 }])}
        onSelectTrack={vi.fn()}
        onResetSelection={vi.fn()}
      />,
    );

    expect(screen.getByText("French")).toBeInTheDocument();
    expect(screen.queryByText("EAC3 5.1")).toBeNull();
    expect(screen.queryByText("Language: Unknown")).toBeNull();
  });
});
