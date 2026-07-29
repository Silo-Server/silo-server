import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { ResolvedSection } from "@/api/types";
import SidebarItemNavigationProvider from "./SidebarItemNavigationProvider";
import NowListeningHero from "./NowListeningHero";

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: () => ({ data: undefined }),
}));
vi.mock("@/hooks/useAmbientColor", () => ({ useAmbientColor: () => undefined }));
vi.mock("@/hooks/useOverlayPrefs", () => ({ useOverlayPrefs: () => ({ prefs: {} }) }));
vi.mock("@/pages/audiobooks/player/audiobookPlaybackContext", () => ({
  useAudiobookPlaybackController: () => null,
}));

describe("NowListeningHero", () => {
  it("routes item details through the sidebar interception path", () => {
    const begin = vi.fn(() => true);
    const section = {
      title: "Continue listening",
      items: [
        {
          content_id: "audiobook-1",
          type: "audiobook",
          title: "The Book",
          position_seconds: 120,
          duration_seconds: 3600,
        },
      ],
    } as unknown as ResolvedSection;

    render(
      <MemoryRouter>
        <SidebarItemNavigationProvider begin={begin} itemDetailsReady>
          <NowListeningHero section={section} libraryId={7} />
        </SidebarItemNavigationProvider>
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("link", { name: "More Info" }));

    expect(begin).toHaveBeenCalledWith({
      href: "/item/audiobook-1?libraryId=7",
      replace: undefined,
      state: undefined,
    });
  });
});
