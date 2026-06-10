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
  deleteMutate: vi.fn(),
  getProfileToken: vi.fn(),
}));

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: (...args: unknown[]) => mocks.useProfiles(...args),
  useDeleteProfile: () => ({
    isPending: false,
    mutate: (...args: unknown[]) => mocks.deleteMutate(...args),
  }),
  useHouseholdSessions: () => ({ data: [], isLoading: false, isFetching: false }),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useAvailableUserLibraries: (...args: unknown[]) => mocks.useAvailableUserLibraries(...args),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: (...args: unknown[]) => mocks.useAuth(...args),
}));

vi.mock("@/api/client", () => ({
  getProfileToken: (...args: unknown[]) => mocks.getProfileToken(...args),
}));

vi.mock("@/components/profiles/ProfileEditorDialog", () => ({
  ProfileEditorDialog: ({
    open,
    profile,
    onSaveSuccess,
  }: {
    open: boolean;
    profile?: Profile | null;
    onSaveSuccess?: (profile: Profile, context: { mode: "create" | "edit"; pin: string }) => void;
  }) =>
    open ? (
      <div data-testid="profile-editor">
        <button
          type="button"
          onClick={() =>
            onSaveSuccess?.(
              profile
                ? {
                    ...profile,
                    name: `${profile.name} Updated`,
                  }
                : makeProfile({
                    id: "profile-created",
                    name: "Created",
                  }),
              {
                mode: profile ? "edit" : "create",
                pin: "",
              },
            )
          }
        >
          save-editor
        </button>
      </div>
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

vi.mock("@/components/ConfirmDialog", () => ({
  ConfirmDialog: ({
    open,
    onConfirm,
    title,
  }: {
    open: boolean;
    onConfirm: () => void;
    title: string;
  }) =>
    open ? (
      <div>
        <span>{title}</span>
        <button type="button" onClick={onConfirm}>
          confirm-delete
        </button>
      </div>
    ) : null,
}));

import ProfilesSettings from "./ProfilesSettings";

const libraries: UserLibrary[] = [
  { id: 1, name: "Movies", type: "movies", sort_order: 0 },
  { id: 2, name: "Series", type: "series", sort_order: 1 },
];

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
  return Array.from(container.querySelectorAll("button")).find(
    (button) => button.textContent?.trim() === label,
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

describe("ProfilesSettings", () => {
  let container: HTMLDivElement;
  let root: Root;
  let selectProfile: ReturnType<typeof vi.fn>;
  let verifyProfilePin: ReturnType<typeof vi.fn>;

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

    mocks.useProfiles.mockReset();
    mocks.useAvailableUserLibraries.mockReset();
    mocks.useAuth.mockReset();
    mocks.deleteMutate.mockReset();
    mocks.getProfileToken.mockReset();

    mocks.useProfiles.mockReturnValue({
      data: [makeProfile()],
      isLoading: false,
    });
    mocks.useAvailableUserLibraries.mockReturnValue({
      data: libraries,
      isLoading: false,
    });
    mocks.useAuth.mockReturnValue({
      profile: makeProfile(),
      selectProfile,
      verifyProfilePin,
    });
    mocks.deleteMutate.mockImplementation((_id: string, options?: { onSuccess?: () => void }) => {
      options?.onSuccess?.();
    });
    mocks.getProfileToken.mockReturnValue(null);
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

  it("renders the profile list with the current badge and access summary", async () => {
    mocks.useProfiles.mockReturnValue({
      data: [
        makeProfile({
          max_content_rating: "PG",
          library_restrictions_enabled: true,
          allowed_library_ids: [1, 2],
          max_playback_quality: "1080p",
        }),
      ],
      isLoading: false,
    });

    await render(<ProfilesSettings />);

    expect(container.textContent).toContain("Profiles");
    expect(container.textContent).toContain("Current");
    expect(container.textContent).toContain("PG max · 2 libraries · Standard quality");
  });

  it("creates a profile without switching the current profile", async () => {
    await render(<ProfilesSettings />);

    await click(findButton(container, "New profile"));
    await click(findButton(container, "save-editor"));

    expect(selectProfile).not.toHaveBeenCalled();
  });

  it("updates the active profile and refreshes auth state", async () => {
    await render(<ProfilesSettings />);

    await click(findButton(container, "Edit"));
    await click(findButton(container, "save-editor"));

    expect(selectProfile).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Main Updated" }),
      undefined,
    );
  });

  it("disables delete for the current profile and for the last remaining profile", async () => {
    await render(<ProfilesSettings />);

    const deleteButton = findButton(container, "Delete");
    expect(deleteButton).toBeDefined();
    expect((deleteButton as HTMLButtonElement).disabled).toBe(true);
    expect(container.textContent).toContain("At least one profile is required.");
  });

  it("shows the current-profile delete guard when other profiles still exist", async () => {
    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), makeProfile({ id: "profile-2", name: "Guest" })],
      isLoading: false,
    });

    await render(<ProfilesSettings />);

    const deleteButtons = Array.from(container.querySelectorAll("button")).filter(
      (button) => button.textContent?.trim() === "Delete",
    );

    expect((deleteButtons[0] as HTMLButtonElement).disabled).toBe(true);
    expect(container.textContent).toContain("Switch to another profile before deleting this one.");
  });

  it("switches to an unlocked profile from the list", async () => {
    const otherProfile = makeProfile({ id: "profile-2", name: "Guest" });
    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), otherProfile],
      isLoading: false,
    });

    await render(<ProfilesSettings />);

    await click(findButton(container, "Use"));

    expect(selectProfile).toHaveBeenCalledWith(otherProfile);
  });

  it("verifies a locked profile before switching to it", async () => {
    const lockedProfile = makeProfile({
      id: "profile-2",
      name: "Kids",
      has_pin: true,
    });
    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), lockedProfile],
      isLoading: false,
    });

    await render(<ProfilesSettings />);

    await click(findButton(container, "Use"));
    await click(findButton(container, "confirm-pin"));

    expect(selectProfile).toHaveBeenCalledWith(lockedProfile, "verified-token");
  });
});
