// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { AdminUser } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import type { AdminProfileSetEdit } from "@/hooks/queries/admin/userProfiles";

import { ProfileSetEditsCard } from "./ProfileSetEditsCard";
import { ProfilesTab } from "./ProfilesTab";

type Outcome = { wake_queued: boolean; applied: boolean } | Error;
type MutateOptions = { onSuccess?: (r: unknown) => void; onError?: (e: unknown) => void };

const mocks = vi.hoisted(() => ({
  edits: [] as AdminProfileSetEdit[],
  pending: false,
  capabilities: { profile_management: true, profile_set_edits: true } as Record<string, boolean>,
  editsEnabled: [] as boolean[],
  outcome: { wake_queued: false, applied: true } as Outcome,
  cleared: [] as unknown[],
}));

vi.mock("@/hooks/queries/admin/userProfiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/queries/admin/userProfiles")>();
  const settle = (opts?: MutateOptions) => {
    if (mocks.outcome instanceof Error) opts?.onError?.(mocks.outcome);
    else opts?.onSuccess?.(mocks.outcome);
  };
  const idle = { isPending: false, mutate: vi.fn() };
  return {
    profileSetEditRefusal: actual.profileSetEditRefusal,
    useAdminUserProfileSetEdits: (_id: number, enabled: boolean) => {
      mocks.editsEnabled.push(enabled);
      return mocks.pending
        ? { isPending: true, isError: false, data: undefined }
        : { isPending: false, isError: false, data: enabled ? mocks.edits : undefined };
    },
    useClearAdminUserProfileSetEdit: () => ({
      isPending: false,
      mutate: (target: unknown, opts?: MutateOptions) => {
        mocks.cleared.push(target);
        settle(opts);
      },
    }),
    useDismissAdminUserProfileSetEdit: () => ({
      isPending: false,
      mutate: (_target: unknown, opts?: MutateOptions) => settle(opts),
    }),
    useAdminUserProfile: () => ({ data: undefined }),
    useCreateAdminUserProfile: () => idle,
    useUpdateAdminUserProfile: () => idle,
    useDeleteAdminUserProfile: () => idle,
  };
});
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUserCapabilities: () => ({ data: mocks.capabilities }),
  useAdminUsers: () => ({ isSuccess: true }),
}));
vi.mock("@/hooks/queries/admin/userActivity", () => ({
  useAdminUserProfileActivity: () => ({
    data: [
      {
        id: "p-main",
        name: "sam",
        is_primary: true,
        is_child: false,
        has_pin: false,
        last_seen_at: null,
      },
      {
        id: "p-kid",
        name: "Kiddo",
        is_primary: false,
        is_child: true,
        has_pin: true,
        last_seen_at: null,
      },
    ],
  }),
  useAdminUserLiveSessions: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/profiles", () => ({ useProfiles: () => ({ data: [] }) }));

const user = {
  id: 2,
  username: "sam",
  effective_policy: { max_profiles: 5, library_ids: null },
} as unknown as AdminUser;

function edit(extra: Partial<AdminProfileSetEdit>): AdminProfileSetEdit {
  return {
    kind: "override",
    sync_id: "0f8fad5b-d9cb-469f-a165-70867728950e",
    profile_id: "p-kid",
    profile_name: "Kiddo",
    field: "max_content_rating",
    by: "owner",
    actor: { id: "1", username: "admin" },
    at: "2026-11-25T03:50:00.000Z",
    value: "G",
    inherited: false,
    set_value: "PG",
    set_resets: false,
    clear_applies: true,
    replaced_owner: false,
    etag: '"pso1-shown"',
    ...extra,
  } as AdminProfileSetEdit;
}

beforeEach(() => {
  mocks.edits = [];
  mocks.pending = false;
  mocks.capabilities = { profile_management: true, profile_set_edits: true };
  mocks.editsEnabled = [];
  mocks.outcome = { wake_queued: false, applied: true };
  mocks.cleared = [];
});
afterEach(cleanup);

async function openClear(name: RegExp) {
  await userEvent.click(screen.getByRole("button", { name }));
  return screen.getByRole("alertdialog");
}

// The PIN's confirmation follows what the server said clearing does: when
// the person's app has no PIN for the profile, clearing takes the owner's
// PIN off now, and the dialog must say so rather than promise it stays.
it("warns that clearing removes the PIN when the app has none", async () => {
  mocks.edits = [edit({ field: "pin", value: "set", set_value: "clear", clear_applies: true })];
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to PIN on Kiddo/);
  expect(within(dialog).getByText(/clearing removes the PIN you set, now/)).toBeInTheDocument();
  expect(within(dialog).getByRole("button", { name: "Clear and remove PIN" })).toBeInTheDocument();
  expect(within(dialog).queryByText(/keeps the PIN you set/)).toBeNull();
});

it("says the PIN stays when the app sent its PIN as digits", async () => {
  mocks.edits = [edit({ field: "pin", value: "set", set_value: "set", clear_applies: false })];
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to PIN on Kiddo/);
  expect(
    within(dialog).getByText(/keeps the PIN you set until their app next sends one/),
  ).toBeInTheDocument();
  expect(within(dialog).getByRole("button", { name: "Clear" })).toBeInTheDocument();
});

it("names the value that comes back for a field the server can restore", async () => {
  mocks.edits = [edit({})];
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to Content rating limit on Kiddo/);
  expect(
    within(dialog).getByText(/goes back to PG, the value from their app, now/),
  ).toBeInTheDocument();
});

// A nullable setting's null is a value ("No preference"), not the text
// "null", in the list and in the confirmation.
it("shows a null setting as the setting's unset label", async () => {
  mocks.edits = [
    edit({ field: "settings.playback.audio_language", value: null, set_value: null } as never),
  ];
  render(<ProfileSetEditsCard user={user} manage />);
  expect(screen.getByText("Preferred audio language: No preference")).toBeInTheDocument();
  const dialog = await openClear(/Clear the change to Preferred audio language on Kiddo/);
  expect(
    within(dialog).getByText(/goes back to No preference, the value from their app/),
  ).toBeInTheDocument();
  expect(screen.queryByText(/null/)).toBeNull();
});

// The clear names the override the owner was shown, so the server can
// refuse it if what clearing does has changed since.
it("sends the etag of the record the dialog showed", async () => {
  mocks.edits = [
    edit({ etag: '"pso1-shown"' } as never),
    edit({
      field: "pin",
      value: "set",
      set_value: "set",
      clear_applies: false,
      etag: '"pso1-other"',
    } as never),
  ];
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to Content rating limit on Kiddo/);
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
  expect(mocks.cleared).toEqual([
    { kind: "override", profileId: "p-kid", field: "max_content_rating", etag: '"pso1-shown"' },
  ]);
});

it("says the change was updated when the server refuses a stale clear", async () => {
  mocks.edits = [edit({ etag: '"pso1-shown"' } as never)];
  mocks.outcome = new V2ProblemError("clearAdminUserProfileOverride", {
    type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
    title: "Precondition failed",
    status: 412,
  } as never);
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to Content rating limit on Kiddo/);
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
  expect(screen.getByRole("status")).toHaveTextContent(
    "This change was updated after you opened it, so nothing was cleared.",
  );
  expect(screen.queryByRole("alertdialog")).toBeNull();
});

// An override listed without an etag can't be cleared on what the owner was
// shown, so the card offers no Clear for it rather than send a clear that
// names no record (or "*").
it("offers no Clear for an override without an etag", () => {
  mocks.edits = [
    edit({ etag: undefined } as never),
    edit({ field: "pin", value: "set", etag: "" } as never),
  ];
  render(<ProfileSetEditsCard user={user} manage />);
  expect(screen.queryByRole("button", { name: /Clear the change/ })).toBeNull();
  expect(mocks.cleared).toEqual([]);
});

// After a stale refusal the owner opens the refreshed record and clears
// again: that clear names the refreshed record's etag, never "*".
it("clears again with the refreshed record's etag after a stale refusal", async () => {
  mocks.edits = [edit({ etag: '"pso1-old"' } as never)];
  mocks.outcome = new V2ProblemError("clearAdminUserProfileOverride", {
    type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
    title: "Precondition failed",
    status: 412,
  } as never);
  const view = render(<ProfileSetEditsCard user={user} manage />);
  let dialog = await openClear(/Clear the change to Content rating limit on Kiddo/);
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
  mocks.edits = [edit({ set_value: "R", etag: '"pso1-new"' } as never)];
  mocks.outcome = { wake_queued: false, applied: true };
  view.rerender(<ProfileSetEditsCard user={user} manage />);
  dialog = await openClear(/Clear the change to Content rating limit on Kiddo/);
  expect(within(dialog).getByText(/goes back to R, the value from their app/)).toBeInTheDocument();
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
  expect(mocks.cleared).toEqual([
    { kind: "override", profileId: "p-kid", field: "max_content_rating", etag: '"pso1-old"' },
    { kind: "override", profileId: "p-kid", field: "max_content_rating", etag: '"pso1-new"' },
  ]);
});

// A refused clear is explained, not reported as a failure.
it("explains a name the server refused to put back", async () => {
  mocks.edits = [edit({ field: "name", value: "Kiddo", set_value: "Kid" })];
  mocks.outcome = new V2ProblemError("clearAdminUserProfileOverride", {
    type: "https://siloserver.org/docs/api/v2/problems/conflict",
    title: "Conflict",
    status: 409,
    detail: "A profile with this name already exists",
  } as never);
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to Name on Kiddo/);
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
  expect(screen.getByRole("status")).toHaveTextContent(
    "Kiddo can’t go back to the name Kid from their app, because another profile here has that name.",
  );
});

it("tells the administrator when a record was already cleared", async () => {
  mocks.edits = [edit({})];
  mocks.outcome = new V2ProblemError("clearAdminUserProfileOverride", {
    type: "https://siloserver.org/docs/api/v2/problems/not_found",
    title: "Not found",
    status: 404,
  } as never);
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to Content rating limit on Kiddo/);
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
  expect(screen.getByRole("status")).toHaveTextContent(
    "That change was already cleared, or sam changed it again.",
  );
});

it.each([
  [true, "Cleared. sam’s phone has been asked to sync."],
  [false, "Cleared. It reaches sam’s app the next time it syncs."],
])("after a clear with wake_queued %s, says %s", async (woken, sentence) => {
  mocks.edits = [edit({})];
  mocks.outcome = { wake_queued: woken, applied: true };
  render(<ProfileSetEditsCard user={user} manage />);
  const dialog = await openClear(/Clear the change to Content rating limit on Kiddo/);
  await userEvent.click(within(dialog).getByRole("button", { name: "Clear" }));
  expect(screen.getByRole("status")).toHaveTextContent(sentence);
});

it("lists a hidden profile with who removed it and lets it back", async () => {
  mocks.edits = [
    edit({
      kind: "hidden",
      profile_id: undefined,
      field: undefined,
      profile_name: "Guest",
      value: undefined,
      set_value: undefined,
      clear_applies: false,
    }),
  ];
  render(<ProfileSetEditsCard user={user} manage />);
  expect(screen.getByText(/Removed on this server by admin/)).toBeInTheDocument();
  const dialog = await openClear(/Let Guest come back on this server/);
  await userEvent.click(within(dialog).getByRole("button", { name: "Allow again" }));
  expect(mocks.cleared).toEqual([
    { kind: "hidden", syncId: "0f8fad5b-d9cb-469f-a165-70867728950e" },
  ]);
});

it("shows a loading state rather than popping in", () => {
  mocks.pending = true;
  render(<ProfileSetEditsCard user={user} manage />);
  expect(screen.getByText("Changed on this server")).toBeInTheDocument();
  expect(document.querySelector('[aria-busy="true"]')).not.toBeNull();
});

// The tab reads the records, and shows the card and badge, only on a server
// that serves them.
it("renders no card and asks for no records when the capability is off", () => {
  mocks.capabilities = { profile_management: true, profile_set_edits: false };
  mocks.edits = [edit({})];
  render(<ProfilesTab user={user} manageable />);
  expect(screen.queryByText("Changed on this server")).toBeNull();
  expect(screen.queryByText("Changed here")).toBeNull();
  expect(mocks.editsEnabled.every((enabled) => !enabled)).toBe(true);
});

it("marks the profile the owner changed when the capability is on", () => {
  mocks.edits = [edit({})];
  render(<ProfilesTab user={user} manageable />);
  expect(screen.getByText("Changed on this server")).toBeInTheDocument();
  expect(screen.getByText("Changed here")).toBeInTheDocument();
});
