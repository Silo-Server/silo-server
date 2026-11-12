import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { Profile, UserLibrary } from "@/api/types";
import type { ProfileCreate, ProfileUpdate } from "@/hooks/queries/profiles";

/**
 * Where the editor saves instead of the signed-in household's own profile
 * operations: the admin account page passes its account's.
 */
export interface ProfileEditorSave {
  create: (body: ProfileCreate) => Promise<Profile>;
  update: (id: string, body: ProfileUpdate) => Promise<Profile>;
  pending: boolean;
}

export interface ProfileEditorDialogProps {
  open: boolean;
  profile?: Profile | null;
  /**
   * Creating this profile makes it the account's primary profile (the account
   * has none yet), so it gets no library limit, as an edited primary doesn't.
   */
  createsPrimary?: boolean;
  libraries: UserLibrary[];
  avatarUploadEnabled?: boolean;
  /** The server accepts `max_advisory_age`; hides the control when false. */
  advisoryAgeSupported?: boolean;
  /** The server accepts `require_advisory_age`; hides the switch when false. */
  requireAdvisoryAgeSupported?: boolean;
  /**
   * Creates the profile and uploads its avatar as this profile instead of the
   * active one, as Who's watching does for the verified primary profile.
   */
  profileContext?: ProfileRequestContextSnapshot;
  /**
   * Saves through these instead. They cannot upload an avatar, so the upload
   * field is hidden; removing an existing upload is saved with the update.
   */
  save?: ProfileEditorSave;
  onOpenChange: (open: boolean) => void;
  /** Extra classes for the dialog's content, e.g. a page's touch-target sizing. */
  className?: string;
  onSaveSuccess?: (
    profile: Profile,
    context: {
      mode: "create" | "edit";
      pin: string;
    },
  ) => void;
}

export interface ValidationErrors {
  name?: string;
  pin?: string;
  libraries?: string;
}

export const ANY_CONTENT_RATING_VALUE = "__any_content__";
export const NO_ADVISORY_AGE_VALUE = "__no_advisory_limit__";

export function sortLibraryIDs(ids: number[]) {
  return [...new Set(ids)].sort((left, right) => left - right);
}
