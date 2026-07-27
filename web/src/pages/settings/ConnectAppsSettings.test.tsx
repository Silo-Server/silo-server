// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { CompatConnectInfo, Profile } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useProfiles: vi.fn(),
  useAuth: vi.fn(),
  useCompatConnectInfo: vi.fn(),
  copyTextToClipboard: vi.fn(),
}));

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: (...args: unknown[]) => mocks.useProfiles(...args),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: (...args: unknown[]) => mocks.useAuth(...args),
}));

vi.mock("@/hooks/queries/compat", () => ({
  useCompatConnectInfo: (...args: unknown[]) => mocks.useCompatConnectInfo(...args),
}));

vi.mock("@/lib/clipboard", () => ({
  copyTextToClipboard: (...args: unknown[]) => mocks.copyTextToClipboard(...args),
}));

import ConnectAppsSettings from "./ConnectAppsSettings";

function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "profile-1",
    name: "Doe Household",
    avatar: "",
    has_pin: false,
    is_child: false,
    is_primary: true,
    max_content_rating: "",
    quality_preference: "auto",
    language: "en",
    subtitle_language: "",
    subtitle_mode: "auto",
    show_forced_subtitles: true,
    auto_skip_intro: false,
    auto_skip_credits: false,
    library_restrictions_enabled: false,
    allowed_library_ids: null,
    max_playback_quality: "",
    created_at: "2026-04-06T00:00:00Z",
    updated_at: "2026-04-06T00:00:00Z",
    ...overrides,
  };
}

function makeConnectInfo(
  overrides: Partial<CompatConnectInfo["jellyfin"]> = {},
): CompatConnectInfo {
  return {
    jellyfin: {
      enabled: true,
      public_url: "https://compat.example.test",
      server_name: "Example",
      ...overrides,
    },
  };
}

function findButton(container: HTMLElement, label: string) {
  return Array.from(container.querySelectorAll("button")).find((button) =>
    button.textContent?.trim().includes(label),
  );
}

async function click(element: Element | undefined) {
  if (!element) {
    throw new Error("element not found");
  }
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("ConnectAppsSettings", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    mocks.useProfiles.mockReset();
    mocks.useAuth.mockReset();
    mocks.useCompatConnectInfo.mockReset();
    mocks.copyTextToClipboard.mockReset();
    mocks.copyTextToClipboard.mockResolvedValue(undefined);

    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), makeProfile({ id: "profile-2", name: "Kids", has_pin: true })],
      isLoading: false,
    });
    mocks.useAuth.mockReturnValue({
      user: { username: "johndoe" },
      profile: { id: "profile-1" },
    });
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo(),
      isLoading: false,
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  function render() {
    act(() => {
      root.render(<ConnectAppsSettings />);
    });
  }

  it("defaults to the Jellyfin tab and shows the account#profile username", () => {
    render();

    expect(container.textContent).toContain("For Jellyfin-compatible apps only");
    expect(container.textContent).toContain("johndoe#Doe Household");
    expect(container.textContent).toContain("https://compat.example.test");
  });

  it("shows the plain account name and the Silo origin on the Silo tab", async () => {
    render();

    await click(findButton(container, "Silo app or website"));

    expect(container.textContent).toContain("For Silo's own apps");
    expect(container.textContent).toContain("Don't add a # to either field here.");
    expect(container.textContent).not.toContain("johndoe#");
    // jsdom's default origin stands in for the deployed server address.
    expect(container.textContent).toContain(window.location.origin);
  });

  it("switches the username and password format when another profile is picked", async () => {
    render();

    expect(container.textContent).toContain("has no PIN — just your account password");

    await click(findButton(container, "Kids"));

    expect(container.textContent).toContain("johndoe#Kids");
    expect(container.textContent).toContain("Kids has a PIN, so append # and the PIN");
  });

  it("copies the compat username rather than the bare account name", async () => {
    render();

    await click(
      Array.from(container.querySelectorAll("button")).find(
        (button) => button.getAttribute("aria-label") === "Copy Username",
      ),
    );

    expect(mocks.copyTextToClipboard).toHaveBeenCalledWith("johndoe#Doe Household");
  });

  it("explains that the compatibility API is off instead of showing credentials", () => {
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo({ enabled: false }),
      isLoading: false,
    });

    render();

    expect(container.textContent).toContain("The Jellyfin compatibility API is turned off");
    expect(container.textContent).not.toContain("https://compat.example.test");
  });

  it("refuses to hand out a username a # in the profile name would break", async () => {
    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), makeProfile({ id: "profile-2", name: "Movie #2" })],
      isLoading: false,
    });

    render();

    await click(findButton(container, "Movie #2"));

    expect(container.textContent).toContain("contains a #, which Jellyfin apps can't sign in with");
    // No copy button, because the displayed string would not authenticate.
    expect(
      Array.from(container.querySelectorAll("button")).find(
        (button) => button.getAttribute("aria-label") === "Copy Username",
      ),
    ).toBeUndefined();
  });

  it("keeps the page usable when no public compat address is configured", () => {
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo({ public_url: "" }),
      isLoading: false,
    });

    render();

    expect(container.textContent).toContain("No public address configured");
    expect(container.textContent).toContain("johndoe#Doe Household");
  });
});
