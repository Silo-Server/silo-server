import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { RefreshCw } from "lucide-react";

import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { Profile, UserLibrary } from "@/api/types";
import { ImageUploadField } from "@/components/ImageUploadField";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  useCreateProfile,
  useDeleteProfileAvatar,
  useUpdateProfile,
  useUploadProfileAvatar,
} from "@/hooks/queries/profiles";
import {
  buildProfileAvatarPresetBatch,
  parseDiceBearPresetId,
  PROFILE_AVATAR_STYLES,
  resolveProfileAvatarImage,
  resolveProfileAvatarPreset,
} from "@/lib/profile-avatars";
import { PLAYBACK_QUALITY_OPTIONS, type PlaybackQualityPreset } from "@/lib/playback-quality";
import {
  ADVISORY_AGE_OPTIONS,
  applyKidsPreset,
  buildProfileRequestFromDraft,
  buildProfileUpdateFromDraft,
  clearKidsPreset,
  CONTENT_RATING_OPTIONS,
  createProfileDraft,
  type ProfileDraft,
} from "@/lib/profile-management";
import { cn } from "@/lib/utils";
import { AllowedTitlesSection } from "@/components/profiles/AllowedTitlesSection";
import { ProfileLibraryAccess } from "@/components/profiles/ProfileLibraryAccess";
import {
  profileHasMaturityLimit,
  useCanManageTitleAllowances,
} from "@/hooks/queries/titleAllowances";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useT } from "@/i18n";
import {
  ANY_CONTENT_RATING_VALUE,
  NO_ADVISORY_AGE_VALUE,
  sortLibraryIDs,
  type ProfileEditorDialogProps,
  type ProfileEditorSave,
  type ValidationErrors,
} from "./ProfileEditorDialog.model";

export type { ProfileEditorSave } from "./ProfileEditorDialog.model";

export function ProfileEditorDialog({
  open,
  profile = null,
  createsPrimary = false,
  libraries,
  avatarUploadEnabled = false,
  advisoryAgeSupported = false,
  requireAdvisoryAgeSupported = false,
  profileContext,
  save,
  onOpenChange,
  onSaveSuccess,
}: ProfileEditorDialogProps) {
  const t = useT();
  const mode = profile ? "edit" : "create";
  const sortedLibraries = useMemo(
    () => [...libraries].sort((left, right) => left.sort_order - right.sort_order),
    [libraries],
  );

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {mode === "edit"
              ? t("web.profiles.editor.titleEdit")
              : t("web.profiles.editor.titleNew")}
          </DialogTitle>
          <DialogDescription>{t("web.profiles.editor.description")}</DialogDescription>
        </DialogHeader>
        {open ? (
          <ProfileEditorForm
            key={profile?.id ?? "new-profile"}
            mode={mode}
            profile={profile}
            isPrimary={profile ? profile.is_primary : createsPrimary}
            libraries={sortedLibraries}
            avatarUploadEnabled={avatarUploadEnabled}
            advisoryAgeSupported={advisoryAgeSupported}
            requireAdvisoryAgeSupported={requireAdvisoryAgeSupported}
            profileContext={profileContext}
            save={save}
            onOpenChange={onOpenChange}
            onSaveSuccess={onSaveSuccess}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function ProfileEditorForm({
  mode,
  profile,
  isPrimary,
  libraries,
  avatarUploadEnabled,
  advisoryAgeSupported,
  requireAdvisoryAgeSupported,
  profileContext,
  save,
  onOpenChange,
  onSaveSuccess,
}: {
  mode: "create" | "edit";
  profile: Profile | null;
  isPrimary: boolean;
  libraries: UserLibrary[];
  avatarUploadEnabled: boolean;
  advisoryAgeSupported: boolean;
  requireAdvisoryAgeSupported: boolean;
  profileContext?: ProfileRequestContextSnapshot;
  save?: ProfileEditorSave;
  onOpenChange: (open: boolean) => void;
  onSaveSuccess?: (
    profile: Profile,
    context: {
      mode: "create" | "edit";
      pin: string;
    },
  ) => void;
}) {
  const t = useT();
  const createMutation = useCreateProfile(profileContext);
  const updateMutation = useUpdateProfile();
  const uploadAvatarMutation = useUploadProfileAvatar(profileContext);
  const deleteAvatarMutation = useDeleteProfileAvatar();
  const isPending =
    save?.pending === true ||
    createMutation.isPending ||
    updateMutation.isPending ||
    uploadAvatarMutation.isPending ||
    deleteAvatarMutation.isPending;

  // The primary profile inherits the account's libraries and hides the ones it
  // doesn't want in Settings → Libraries, so it has no library limit. A limit
  // an older server stored on it is cleared by the next save.
  const [draft, setDraft] = useState<ProfileDraft>(() => {
    const initial = createProfileDraft(profile);
    return isPrimary
      ? { ...initial, libraryRestrictionsEnabled: false, allowedLibraryIDs: [] }
      : initial;
  });
  const [avatarFile, setAvatarFile] = useState<File | null>(null);
  const [removeUploadedAvatar, setRemoveUploadedAvatar] = useState(false);
  const [activePresetStyle, setActivePresetStyle] = useState(
    () =>
      parseDiceBearPresetId(draft.avatarPreset)?.styleId ??
      PROFILE_AVATAR_STYLES[0]?.id ??
      "identicon",
  );
  const [presetBatch, setPresetBatch] = useState(0);
  const [errors, setErrors] = useState<ValidationErrors>({});
  const [contentRatingTouched, setContentRatingTouched] = useState(false);
  const [libraryAccessTouched, setLibraryAccessTouched] = useState(false);
  // Allowances belong to the signed-in household, so not when saving another
  // account's profiles (save), and only for another profile with a limit.
  const canManageAllowances = useCanManageTitleAllowances();
  const { profile: activeProfile } = useCurrentProfile();
  const showAllowedTitles =
    canManageAllowances &&
    mode === "edit" &&
    !save &&
    profile !== null &&
    profile.id !== activeProfile?.id &&
    profileHasMaturityLimit(profile);

  const nameId = useId();
  const pinId = useId();
  const contentRatingId = useId();
  const advisoryAgeId = useId();
  const advisoryAgeHelpId = useId();
  const requireAdvisoryAgeId = useId();
  const requireAdvisoryAgeHelpId = useId();
  const playbackQualityId = useId();
  const selectedContentRatingValue =
    draft.maxContentRating === "" ? ANY_CONTENT_RATING_VALUE : draft.maxContentRating;

  const selectedPreset = resolveProfileAvatarPreset(draft.avatarPreset);
  const visiblePresets = useMemo(
    () => buildProfileAvatarPresetBatch(activePresetStyle, presetBatch),
    [activePresetStyle, presetBatch],
  );
  const filePreviewURL = useMemo(
    () => (avatarFile ? URL.createObjectURL(avatarFile) : ""),
    [avatarFile],
  );
  const existingUploadedAvatarURL =
    !removeUploadedAvatar && profile?.avatar_source === "upload"
      ? resolveProfileAvatarImage(profile)
      : "";
  const previewImage = filePreviewURL || selectedPreset?.previewUrl || existingUploadedAvatarURL;

  useEffect(() => {
    return () => {
      if (filePreviewURL) {
        URL.revokeObjectURL(filePreviewURL);
      }
    };
  }, [filePreviewURL]);

  function updateDraft<K extends keyof ProfileDraft>(key: K, value: ProfileDraft[K]) {
    setDraft((current) => ({
      ...current,
      [key]: value,
    }));
  }

  function selectPreset(presetID: string) {
    setRemoveUploadedAvatar(true);
    updateDraft("avatarPreset", draft.avatarPreset === presetID ? "" : presetID);
  }

  function toggleLibrary(libraryID: number, checked: boolean) {
    setLibraryAccessTouched(true);
    setErrors((current) => ({ ...current, libraries: undefined }));
    setDraft((current) => ({
      ...current,
      allowedLibraryIDs: checked
        ? sortLibraryIDs([...current.allowedLibraryIDs, libraryID])
        : current.allowedLibraryIDs.filter((id) => id !== libraryID),
    }));
  }

  function handleKidsChange(checked: boolean) {
    if (!checked) {
      setContentRatingTouched(false);
      setLibraryAccessTouched(false);
      setErrors((current) => ({ ...current, libraries: undefined }));
      setDraft((current) => clearKidsPreset(current));
      return;
    }

    setDraft((current) =>
      applyKidsPreset(current, {
        contentRatingTouched,
        libraryAccessTouched: libraryAccessTouched || isPrimary,
      }),
    );
  }

  function validate(current: ProfileDraft): ValidationErrors {
    const nextErrors: ValidationErrors = {};

    if (current.name.trim() === "") {
      nextErrors.name = t("web.profiles.editor.nameRequired");
    }

    if (current.pin.trim() !== "" && !/^\d{4}$/.test(current.pin.trim())) {
      nextErrors.pin = t("web.profiles.editor.pinInvalid");
    }

    if (current.libraryRestrictionsEnabled && current.allowedLibraryIDs.length === 0) {
      nextErrors.libraries = t("web.profiles.editor.librariesRequired");
    }

    return nextErrors;
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const nextErrors = validate(draft);
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      return;
    }

    const pin = draft.pin.trim();
    const preserveExistingUpload =
      mode === "edit" &&
      profile?.avatar_source === "upload" &&
      !removeUploadedAvatar &&
      !avatarFile &&
      draft.avatarPreset === "";
    // With save, the update removes the upload itself (avatar: null); the
    // household's own editor deletes it through the avatar operation.
    const deleteExistingUpload =
      !save &&
      mode === "edit" &&
      profile?.avatar_source === "upload" &&
      removeUploadedAvatar &&
      !avatarFile &&
      draft.avatarPreset === "";

    let savedProfile: Profile;
    try {
      if (mode === "edit" && profile) {
        const body = buildProfileUpdateFromDraft(draft, {
          advisoryAgeSupported,
          requireAdvisoryAgeSupported,
        });
        if (preserveExistingUpload || deleteExistingUpload) {
          delete body.avatar;
        }
        savedProfile = save
          ? await save.update(profile.id, body)
          : await updateMutation.mutateAsync({ id: profile.id, body });
      } else {
        const body = buildProfileRequestFromDraft(draft, {
          advisoryAgeSupported,
          requireAdvisoryAgeSupported,
        });
        savedProfile = save ? await save.create(body) : await createMutation.mutateAsync(body);
      }
    } catch {
      return;
    }

    let finalProfile = savedProfile;
    if (avatarFile) {
      try {
        finalProfile = await uploadAvatarMutation.mutateAsync({
          id: savedProfile.id,
          file: avatarFile,
        });
      } catch {
        finalProfile = savedProfile;
      }
    } else if (deleteExistingUpload) {
      try {
        finalProfile = await deleteAvatarMutation.mutateAsync(savedProfile);
      } catch {
        finalProfile = savedProfile;
      }
    }

    onSaveSuccess?.(finalProfile, { mode, pin });
    onOpenChange(false);
  }

  return (
    <form onSubmit={(event) => void handleSubmit(event)} className="space-y-5">
      <section className="border-border space-y-4 rounded-md border p-4">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold">{t("web.profiles.editor.profileSection")}</h3>
          <p className="text-muted-foreground text-sm">
            {t("web.profiles.editor.profileSectionHint")}
          </p>
        </div>

        <div className="flex flex-col gap-4 lg:flex-row">
          <div className="flex flex-col items-center gap-3 rounded-xl border px-5 py-4 lg:w-52">
            <Avatar className="ring-border h-24 w-24 ring-2">
              {previewImage ? (
                <AvatarImage
                  src={previewImage}
                  alt={draft.name || t("web.profiles.editor.avatarAlt")}
                />
              ) : null}
              <AvatarFallback className="bg-surface text-primary text-3xl font-bold">
                {(draft.name || profile?.name || "?").charAt(0).toUpperCase()}
              </AvatarFallback>
            </Avatar>
            <div className="text-center">
              <div className="text-sm font-medium">
                {draft.name.trim() || t("web.profiles.editor.preview")}
              </div>
              <div className="text-muted-foreground text-xs">
                {avatarFile
                  ? t("web.profiles.editor.customUploadSelected")
                  : selectedPreset
                    ? selectedPreset.label
                    : existingUploadedAvatarURL
                      ? t("web.profiles.editor.customUpload")
                      : t("web.profiles.editor.initialsFallback")}
              </div>
            </div>
          </div>

          <div className="flex-1 space-y-4">
            <div className="space-y-2">
              <Label htmlFor={nameId}>{t("web.profiles.editor.name")}</Label>
              <Input
                id={nameId}
                value={draft.name}
                onChange={(event) => {
                  updateDraft("name", event.target.value);
                  setErrors((current) => ({ ...current, name: undefined }));
                }}
                required
              />
              {errors.name ? <p className="text-destructive text-sm">{errors.name}</p> : null}
            </div>

            <div className="space-y-2">
              <Label htmlFor={pinId}>
                {draft.clearPin
                  ? t("web.profiles.editor.pinRemoved")
                  : profile?.has_pin
                    ? t("web.profiles.editor.newPin")
                    : t("web.profiles.editor.pinOptional")}
              </Label>
              <Input
                id={pinId}
                type="password"
                inputMode="numeric"
                maxLength={4}
                placeholder={
                  draft.clearPin
                    ? t("web.profiles.editor.pinRemovedOnSave")
                    : t("web.profiles.editor.pinPlaceholder")
                }
                value={draft.clearPin ? "" : draft.pin}
                disabled={draft.clearPin}
                onChange={(event) => {
                  updateDraft("pin", event.target.value.replace(/\D/g, "").slice(0, 4));
                  setErrors((current) => ({ ...current, pin: undefined }));
                }}
              />
              {mode === "edit" && profile?.has_pin ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="text-muted-foreground hover:text-foreground h-7 px-2 text-xs"
                  onClick={() => {
                    const next = !draft.clearPin;
                    setDraft((current) => ({
                      ...current,
                      clearPin: next,
                      pin: next ? "" : current.pin,
                    }));
                    setErrors((current) => ({ ...current, pin: undefined }));
                  }}
                >
                  {draft.clearPin
                    ? t("web.profiles.editor.keepPin")
                    : t("web.profiles.editor.removePin")}
                </Button>
              ) : null}
              {errors.pin ? <p className="text-destructive text-sm">{errors.pin}</p> : null}
            </div>
          </div>
        </div>

        <div className="space-y-3">
          <div className="space-y-1">
            <Label>{t("web.profiles.editor.presets")}</Label>
            <p className="text-muted-foreground text-xs">{t("web.profiles.editor.presetsHint")}</p>
          </div>
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
            {PROFILE_AVATAR_STYLES.map((style) => {
              const selected = style.id === activePresetStyle;

              return (
                <button
                  key={style.id}
                  type="button"
                  onClick={() => {
                    setActivePresetStyle(style.id);
                    setPresetBatch(0);
                  }}
                  className={cn(
                    "rounded-2xl border px-4 py-3 text-start transition-colors",
                    selected
                      ? "border-primary bg-primary/8 text-foreground"
                      : "border-border hover:border-primary/40",
                  )}
                >
                  <div className="text-sm font-medium">{style.label}</div>
                  <div className="text-muted-foreground mt-1 text-xs">{style.summary}</div>
                </button>
              );
            })}
          </div>
          <div className="flex items-center justify-between gap-3">
            <p className="text-muted-foreground text-xs">
              {t("web.profiles.editor.presetsShowing", {
                count: visiblePresets.length,
                count_text: String(visiblePresets.length),
                summary:
                  PROFILE_AVATAR_STYLES.find((style) => style.id === activePresetStyle)?.summary ??
                  "",
              })}
            </p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setPresetBatch((current) => current + 1)}
            >
              <RefreshCw className="h-4 w-4" />
              {t("web.profiles.editor.moreOptions")}
            </Button>
          </div>
          <div className="grid grid-cols-3 gap-3 sm:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
            {visiblePresets.map((preset) => {
              const selected = preset.id === draft.avatarPreset;

              return (
                <button
                  key={preset.id}
                  type="button"
                  onClick={() => selectPreset(preset.id)}
                  className={cn(
                    "flex aspect-square w-full max-w-20 items-center justify-center justify-self-center rounded-2xl border p-2 transition-colors",
                    selected
                      ? "border-primary bg-primary/5"
                      : "border-border hover:border-primary/50",
                  )}
                  aria-pressed={selected}
                  title={preset.label}
                >
                  <div className="size-16 overflow-hidden rounded-xl">
                    <img
                      src={preset.previewUrl}
                      alt={preset.label}
                      className="block h-full w-full object-cover"
                    />
                  </div>
                </button>
              );
            })}
          </div>
        </div>

        {save ? null : avatarUploadEnabled ? (
          <ImageUploadField
            label={t("web.profiles.editor.customUpload")}
            currentUrl={existingUploadedAvatarURL}
            file={avatarFile}
            onFileChange={(file) => {
              setAvatarFile(file);
              if (file) {
                setRemoveUploadedAvatar(true);
              } else if (profile?.avatar_source === "upload") {
                setRemoveUploadedAvatar(false);
              }
            }}
            onDelete={
              existingUploadedAvatarURL
                ? () => {
                    setAvatarFile(null);
                    setRemoveUploadedAvatar(true);
                  }
                : undefined
            }
          />
        ) : (
          <div className="rounded-md border border-dashed px-4 py-3 text-sm">
            <p className="font-medium">{t("web.profiles.editor.uploadsUnavailable")}</p>
            <p className="text-muted-foreground mt-1 text-xs">
              {t("web.profiles.editor.uploadsUnavailableHint")}
            </p>
          </div>
        )}
      </section>

      <section className="border-border space-y-4 rounded-md border p-4">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold">{t("web.profiles.editor.accessSection")}</h3>
          <p className="text-muted-foreground text-sm">
            {t("web.profiles.editor.accessSectionHint")}
          </p>
        </div>

        <div className="border-border flex items-center justify-between rounded-md border px-3 py-2">
          <div className="space-y-0.5">
            <Label htmlFor="profile-is-child">{t("web.profiles.editor.kids")}</Label>
            <p className="text-muted-foreground text-xs">{t("web.profiles.editor.kidsHint")}</p>
          </div>
          <Switch
            id="profile-is-child"
            checked={draft.isChild}
            onCheckedChange={handleKidsChange}
          />
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor={contentRatingId}>{t("web.profiles.editor.maxContentRating")}</Label>
            <Select
              value={selectedContentRatingValue}
              onValueChange={(value) => {
                setContentRatingTouched(true);
                updateDraft("maxContentRating", value === ANY_CONTENT_RATING_VALUE ? "" : value);
              }}
            >
              <SelectTrigger id={contentRatingId} className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CONTENT_RATING_OPTIONS.map((option) => (
                  <SelectItem
                    key={option.value || ANY_CONTENT_RATING_VALUE}
                    value={option.value === "" ? ANY_CONTENT_RATING_VALUE : option.value}
                  >
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {advisoryAgeSupported ? (
            <div className="space-y-2">
              <Label htmlFor={advisoryAgeId}>{t("web.profiles.editor.maxAdvisoryAge")}</Label>
              <Select
                value={
                  draft.maxAdvisoryAge === null
                    ? NO_ADVISORY_AGE_VALUE
                    : String(draft.maxAdvisoryAge)
                }
                onValueChange={(value) =>
                  updateDraft(
                    "maxAdvisoryAge",
                    value === NO_ADVISORY_AGE_VALUE ? null : Number(value),
                  )
                }
              >
                <SelectTrigger
                  id={advisoryAgeId}
                  className="w-full"
                  aria-describedby={advisoryAgeHelpId}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ADVISORY_AGE_OPTIONS.map((option) => (
                    <SelectItem
                      key={option.value ?? NO_ADVISORY_AGE_VALUE}
                      value={option.value === null ? NO_ADVISORY_AGE_VALUE : String(option.value)}
                    >
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p id={advisoryAgeHelpId} className="text-muted-foreground text-xs">
                {requireAdvisoryAgeSupported &&
                draft.maxAdvisoryAge !== null &&
                draft.requireAdvisoryAge
                  ? t("web.profiles.editor.advisoryAgeHelp")
                  : t("web.profiles.editor.advisoryAgeHelpFallback")}
              </p>
              {requireAdvisoryAgeSupported && draft.maxAdvisoryAge !== null ? (
                <div className="flex items-start justify-between gap-3 pt-1">
                  <div className="space-y-0.5">
                    <Label htmlFor={requireAdvisoryAgeId}>
                      {t("web.profiles.editor.hideWithoutAge")}
                    </Label>
                    <p id={requireAdvisoryAgeHelpId} className="text-muted-foreground text-xs">
                      {t("web.profiles.editor.hideWithoutAgeHint")}
                    </p>
                  </div>
                  <Switch
                    id={requireAdvisoryAgeId}
                    aria-describedby={requireAdvisoryAgeHelpId}
                    checked={draft.requireAdvisoryAge}
                    onCheckedChange={(checked) => updateDraft("requireAdvisoryAge", checked)}
                  />
                </div>
              ) : null}
            </div>
          ) : null}

          <div className="space-y-2">
            <Label htmlFor={playbackQualityId}>{t("web.profiles.editor.maxPlaybackQuality")}</Label>
            <Select
              value={draft.maxPlaybackQuality}
              onValueChange={(value) =>
                updateDraft("maxPlaybackQuality", value as PlaybackQualityPreset)
              }
            >
              <SelectTrigger id={playbackQualityId} className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PLAYBACK_QUALITY_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        <ProfileLibraryAccess
          isPrimary={isPrimary}
          libraries={libraries}
          restricted={draft.libraryRestrictionsEnabled}
          allowedLibraryIDs={draft.allowedLibraryIDs}
          error={errors.libraries}
          onRestrictedChange={(checked) => {
            setLibraryAccessTouched(true);
            updateDraft("libraryRestrictionsEnabled", checked);
            setErrors((current) => ({ ...current, libraries: undefined }));
          }}
          onLibraryChange={toggleLibrary}
        />
      </section>

      {showAllowedTitles && profile ? <AllowedTitlesSection profileId={profile.id} /> : null}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
          {t("common.cancel")}
        </Button>
        <Button type="submit" disabled={isPending}>
          {isPending ? t("web.profiles.editor.saving") : t("web.profiles.editor.save")}
        </Button>
      </DialogFooter>
    </form>
  );
}
