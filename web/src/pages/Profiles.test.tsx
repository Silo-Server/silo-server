// @vitest-environment jsdom

import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Profile, UserLibrary } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useProfiles: vi.fn(),
  useAvailableUserLibraries: vi.fn(),
  useAuth: vi.fn(),
  navigate: vi.fn(),
  editorPin: "",
  searchParams: new URLSearchParams(),
}));

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return {
    ...actual,
    useNavigate: () => mocks.navigate,
    useSearchParams: () => [mocks.searchParams, vi.fn()],
  };
});

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: (...args: unknown[]) => mocks.useProfiles(...args),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useAvailableUserLibraries: (...args: unknown[]) => mocks.useAvailableUserLibraries(...args),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: (...args: unknown[]) => mocks.useAuth(...args),
}));

vi.mock("@/components/profiles/ProfileEditorDialog", () => ({
  ProfileEditorDialog: ({
    open,
    onSaveSuccess,
  }: {
    open: boolean;
    onSaveSuccess?: (profile: Profile, context: { mode: "create" | "edit"; pin: string }) => void;
  }) =>
    open ? (
      <button
        type="button"
        onClick={() =>
          onSaveSuccess?.(
            makeProfile({
              id: "created-profile",
              name: "Created",
              has_pin: mocks.editorPin !== "",
            }),
            {
              mode: "create",
              pin: mocks.editorPin,
            },
          )
        }
      >
        save-editor
      </button>
    ) : null,
}));

vi.mock("@/components/profiles/ProfilePinDialog", () => ({
  ProfilePinDialog: ({
    profile,
    onVerified,
  }: {
    profile: Profile | null;
    onVerified: (profile: Profile, token: string) => void;
  }) =>
    profile ? (
      <button type="button" onClick={() => onVerified(profile, "verified-token")}>
        confirm-pin
      </button>
    ) : null,
}));

import Profiles from "./Profiles";

const libraries: UserLibrary[] = [{ id: 1, name: "Movies", type: "movies", sort_order: 0 }];

function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "profile-1",
    name: "Main",
    avatar: "",
    has_pin: false,
    is_child: false,
    is_primary: false,
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

describe("Profiles", () => {
  let container: HTMLDivElement;
  let root: Root;
  let selectProfile: ReturnType<typeof vi.fn>;
  let verifyProfilePin: ReturnType<typeof vi.fn>;
  let logout: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    selectProfile = vi.fn();
    verifyProfilePin = vi.fn().mockResolvedValue({
      valid: true,
      profile_token: "verified-token",
    });
    logout = vi.fn();

    mocks.useProfiles.mockReset();
    mocks.useAvailableUserLibraries.mockReset();
    mocks.useAuth.mockReset();
    mocks.navigate.mockReset();
    mocks.editorPin = "";
    mocks.searchParams = new URLSearchParams();

    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), makeProfile({ id: "profile-2", name: "Guest" })],
      isLoading: false,
    });
    mocks.useAvailableUserLibraries.mockReturnValue({
      data: libraries,
      isLoading: false,
    });
    mocks.useAuth.mockReturnValue({
      selectProfile,
      verifyProfilePin,
      logout,
    });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  async function render(ui: ReactNode) {
    await act(async () => {
      root.render(ui);
    });
  }

  it("keeps the selector focused on profile choice instead of profile management", async () => {
    await render(<Profiles />);

    expect(container.textContent).toContain("Who's watching?");
    expect(container.textContent).not.toContain("Add Profile");
    expect(container.textContent).not.toContain("Edit Profile");
    expect(container.textContent).not.toContain("Delete profile");
  });

  it("creates and selects a profile from the empty state when no PIN is set", async () => {
    mocks.useProfiles.mockReturnValue({
      data: [],
      isLoading: false,
    });

    await render(<Profiles />);

    await click(findButton(container, "Create profile"));
    await click(findButton(container, "save-editor"));

    expect(selectProfile).toHaveBeenCalledWith(
      expect.objectContaining({ id: "created-profile", name: "Created" }),
    );
    expect(mocks.navigate).toHaveBeenCalledWith("/");
  });

  it("verifies a newly created locked profile before selecting it", async () => {
    mocks.useProfiles.mockReturnValue({
      data: [],
      isLoading: false,
    });
    mocks.editorPin = "1234";

    await render(<Profiles />);

    await click(findButton(container, "Create profile"));
    await click(findButton(container, "save-editor"));

    expect(verifyProfilePin).toHaveBeenCalledWith("created-profile", "1234");
    expect(selectProfile).toHaveBeenCalledWith(
      expect.objectContaining({ id: "created-profile" }),
      "verified-token",
    );
    expect(mocks.navigate).toHaveBeenCalledWith("/");
  });

  it("verifies locked profiles before entering the app", async () => {
    const lockedProfile = makeProfile({
      id: "profile-2",
      name: "Kids",
      has_pin: true,
    });
    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), lockedProfile],
      isLoading: false,
    });

    await render(<Profiles />);

    await click(findButton(container, "Kids"));
    await click(findButton(container, "confirm-pin"));

    expect(selectProfile).toHaveBeenCalledWith(lockedProfile, "verified-token");
    expect(mocks.navigate).toHaveBeenCalledWith("/");
  });

  it("returns to the sanitized redirect target after selecting a profile", async () => {
    mocks.searchParams = new URLSearchParams("redirect=%2Frooms%2Fabc%3Froom_token%3Dxyz");

    await render(<Profiles />);

    await click(findButton(container, "Main"));

    expect(selectProfile).toHaveBeenCalled();
    expect(mocks.navigate).toHaveBeenCalledWith("/rooms/abc?room_token=xyz");
  });

  it("ignores unsafe redirect targets when selecting a profile", async () => {
    mocks.searchParams = new URLSearchParams("redirect=https%3A%2F%2Fevil.example");

    await render(<Profiles />);

    await click(findButton(container, "Main"));

    expect(mocks.navigate).toHaveBeenCalledWith("/");
  });
});
