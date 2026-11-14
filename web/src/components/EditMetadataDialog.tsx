import { useState, useMemo, useCallback } from "react";
import { Lock, Unlock } from "lucide-react";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { MetadataTranslatePanel } from "@/components/MetadataTranslatePanel";
import { TagInput } from "@/components/TagInput";
import ImageSelectorTab from "@/components/ImageSelectorTab";
import type { ItemDetail } from "@/api/types";
import {
  useUpdateItemMetadata,
  useRefreshItemMetadata,
  type UpdateItemMetadataRequest,
} from "@/hooks/queries/items";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { useT, type MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";
import {
  AIR_TIMEZONES,
  EXAMPLE_IMDB_ID,
  EXAMPLE_TIMEZONE,
  FIELD_IMAGES,
  FIELD_LOCK_MAP,
  FIELD_NAME,
  FIELD_OVERVIEW,
  FIELD_RELEASE_DATES,
  FIELD_RUNTIME,
  initFormState,
  SECTIONS,
  type Section,
} from "./EditMetadataDialog.form";

// A season or episode row locks only what it stores and a refresh writes. Its
// artwork lock lives on the parent series, and its numbers can't lock.
const SEASON_FIELD_LOCK_MAP: Record<string, number> = {
  title: FIELD_NAME,
  overview: FIELD_OVERVIEW,
  air_date: FIELD_RELEASE_DATES,
};
const EPISODE_FIELD_LOCK_MAP: Record<string, number> = {
  ...SEASON_FIELD_LOCK_MAP,
  runtime: FIELD_RUNTIME,
};

const RESET_DESCRIPTION_KEYS: Partial<Record<ItemDetail["type"], MessageKey>> = {
  series: "web.item.editMetadata.resetDescriptionSeries",
  season: "web.item.editMetadata.resetDescriptionSeason",
  episode: "web.item.editMetadata.resetDescriptionEpisode",
};

const FIELD_LOCK_MAPS: Partial<Record<ItemDetail["type"], Record<string, number>>> = {
  movie: FIELD_LOCK_MAP,
  series: FIELD_LOCK_MAP,
  season: SEASON_FIELD_LOCK_MAP,
  episode: EPISODE_FIELD_LOCK_MAP,
};

interface EditMetadataDialogProps {
  item: ItemDetail;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export default function EditMetadataDialog({ item, open, onOpenChange }: EditMetadataDialogProps) {
  const t = useT();
  const [activeSection, setActiveSection] = useState<Section>("general");
  const [form, setForm] = useState(() => initFormState(item));
  // Keep only local changes to locks. Image selection updates the item while
  // this dialog is open, so a snapshot of the original locks can go stale.
  const [lockOverrides, setLockOverrides] = useState<Map<number, boolean>>(() => new Map());
  const [imageApplyPending, setImageApplyPending] = useState(false);
  const [showResetConfirm, setShowResetConfirm] = useState(false);

  const updateMutation = useUpdateItemMetadata(item.content_id);
  const refreshMutation = useRefreshItemMetadata();

  const ownsImageLock = item.type === "movie" || item.type === "series";
  // A season or episode has the series' content rating, which decides age limits.
  const ownsContentRating = item.type !== "season" && item.type !== "episode";
  const fieldLockMap = FIELD_LOCK_MAPS[item.type];
  const canEditImages = useIsActingAdmin();
  const visibleSections = SECTIONS.filter(
    (s) => s.types.includes(item.type) && (s.key !== "images" || canEditImages),
  );
  const effectiveActiveSection = visibleSections.some((section) => section.key === activeSection)
    ? activeSection
    : "general";

  const originalForm = useMemo(() => initFormState(item), [item]);
  const lockedFields = useMemo(() => {
    const fields = new Set(item.locked_fields ?? []);
    for (const [field, locked] of lockOverrides) {
      if (locked) fields.add(field);
      else fields.delete(field);
    }
    return fields;
  }, [item.locked_fields, lockOverrides]);

  const setField = useCallback(
    (field: string, value: unknown) => {
      setForm((prev) => ({ ...prev, [field]: value }));
      const lockField = fieldLockMap?.[field];
      if (lockField !== undefined) {
        setLockOverrides((prev) => new Map(prev).set(lockField, true));
      }
    },
    [fieldLockMap],
  );

  const toggleLock = useCallback(
    (metadataField: number) => {
      setLockOverrides((prev) =>
        new Map(prev).set(metadataField, !lockedFields.has(metadataField)),
      );
    },
    [lockedFields],
  );

  const handleImageApplied = useCallback(() => {
    if (ownsImageLock) {
      setLockOverrides((prev) => new Map(prev).set(FIELD_IMAGES, true));
    }
  }, [ownsImageLock]);

  const lockedCount = lockedFields.size;

  function handleSave() {
    const data: UpdateItemMetadataRequest = {};

    if (form.title !== originalForm.title) data.title = form.title;
    if (form.sort_title !== originalForm.sort_title) data.sort_title = form.sort_title;
    if (form.original_title !== originalForm.original_title)
      data.original_title = form.original_title;
    if (form.overview !== originalForm.overview) data.overview = form.overview;
    if (form.tagline !== originalForm.tagline) data.tagline = form.tagline;
    if (form.content_rating !== originalForm.content_rating)
      data.content_rating = form.content_rating;
    if (form.year !== originalForm.year) data.year = form.year;
    if (form.runtime !== originalForm.runtime) data.runtime = form.runtime;
    if (JSON.stringify(form.genres) !== JSON.stringify(originalForm.genres))
      data.genres = form.genres;
    if (JSON.stringify(form.studios) !== JSON.stringify(originalForm.studios))
      data.studios = form.studios;
    if (JSON.stringify(form.networks) !== JSON.stringify(originalForm.networks))
      data.networks = form.networks;
    if (JSON.stringify(form.countries) !== JSON.stringify(originalForm.countries))
      data.countries = form.countries;
    if (form.release_date !== originalForm.release_date)
      data.release_date = form.release_date || null;
    if (form.first_air_date !== originalForm.first_air_date)
      data.first_air_date = form.first_air_date || null;
    if (form.last_air_date !== originalForm.last_air_date)
      data.last_air_date = form.last_air_date || null;
    if (form.air_time !== originalForm.air_time) data.air_time = form.air_time || null;
    if (form.air_timezone !== originalForm.air_timezone)
      data.air_timezone = form.air_timezone || null;
    if (form.air_date !== originalForm.air_date) data.air_date = form.air_date || null;
    if (form.rating_imdb !== originalForm.rating_imdb) data.rating_imdb = form.rating_imdb;
    if (form.rating_tmdb !== originalForm.rating_tmdb) data.rating_tmdb = form.rating_tmdb;
    if (form.rating_rt_critic !== originalForm.rating_rt_critic)
      data.rating_rt_critic = form.rating_rt_critic;
    if (form.rating_rt_audience !== originalForm.rating_rt_audience)
      data.rating_rt_audience = form.rating_rt_audience;
    if (form.imdb_id !== originalForm.imdb_id) data.imdb_id = form.imdb_id;
    if (form.tmdb_id !== originalForm.tmdb_id) data.tmdb_id = form.tmdb_id;
    if (form.tvdb_id !== originalForm.tvdb_id) data.tvdb_id = form.tvdb_id;
    if (form.season_number !== originalForm.season_number) data.season_number = form.season_number;
    if (form.episode_number !== originalForm.episode_number)
      data.episode_number = form.episode_number;

    if (fieldLockMap && lockOverrides.size > 0) {
      const originalLocked = new Set(item.locked_fields ?? []);
      const currentLocked = Array.from(lockedFields).sort();
      const originalSorted = Array.from(originalLocked).sort();
      if (JSON.stringify(currentLocked) !== JSON.stringify(originalSorted)) {
        data.locked_fields = currentLocked;
      }
    }

    if (Object.keys(data).length === 0) {
      onOpenChange(false);
      return;
    }

    updateMutation.mutate(data, {
      onSuccess: () => onOpenChange(false),
    });
  }

  function handleReset() {
    setShowResetConfirm(false);
    // The server clears the locks in the reset's own write, so a reset that
    // fails keeps them.
    onOpenChange(false);
    refreshMutation.mutate({ item, mode: "reset" });
  }

  function renderLockIcon(fieldName: string) {
    const metadataField = fieldLockMap?.[fieldName];
    if (metadataField === undefined) return null;
    const isLocked = lockedFields.has(metadataField);
    return (
      <button
        type="button"
        onClick={() => toggleLock(metadataField)}
        className={cn(
          "inline-flex items-center gap-1 text-[10px] transition-colors",
          isLocked
            ? "text-amber-500/80 hover:text-amber-500"
            : "text-muted-foreground/30 hover:text-muted-foreground/60",
        )}
        title={
          isLocked
            ? t("web.item.editMetadata.lockedTitle")
            : t("web.item.editMetadata.unlockedTitle")
        }
      >
        {isLocked ? <Lock className="size-3" /> : <Unlock className="size-3" />}
        {isLocked ? t("web.item.editMetadata.locked") : ""}
      </button>
    );
  }

  const typeLabel = t(
    item.type === "movie"
      ? "web.item.editMetadata.typeMovie"
      : item.type === "series"
        ? "web.item.editMetadata.typeSeries"
        : item.type === "season"
          ? "web.item.editMetadata.typeSeason"
          : "web.item.editMetadata.typeEpisode",
  );

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent
          className="flex max-w-5xl flex-col gap-0 overflow-hidden p-0 sm:max-w-5xl"
          showCloseButton
        >
          {/* Header */}
          <DialogHeader className="border-border/10 shrink-0 flex-row items-center justify-between border-b px-5 py-4 pe-12">
            <div className="flex items-center gap-2.5">
              <DialogTitle className="text-[15px] font-semibold">
                {t("web.item.dialog.editMetadataTitle")}
              </DialogTitle>
              <span className="bg-muted/50 text-muted-foreground rounded px-2 py-0.5 text-[11px]">
                {typeLabel}
              </span>
            </div>
            {fieldLockMap && lockedCount > 0 && (
              <span className="text-muted-foreground flex items-center gap-1 text-[11px]">
                <Lock className="size-3" />
                {t("web.item.editMetadata.lockedCount", { count: lockedCount })}
              </span>
            )}
          </DialogHeader>

          <div
            className="flex min-h-0 flex-1 flex-col sm:flex-row"
            style={{ height: "min(70vh, 580px)" }}
          >
            {/* Sidebar — horizontal tabs on mobile, vertical on sm+ */}
            <nav className="border-border/10 flex flex-shrink-0 overflow-x-auto border-b bg-black/10 sm:w-[160px] sm:flex-col sm:overflow-x-visible sm:border-e sm:border-b-0 sm:py-2">
              {visibleSections.map((section) => (
                <button
                  key={section.key}
                  type="button"
                  onClick={() => setActiveSection(section.key)}
                  className={cn(
                    "px-4 py-2 text-start text-[13px] font-medium whitespace-nowrap transition-colors",
                    effectiveActiveSection === section.key
                      ? "border-primary bg-primary/8 text-primary max-sm:border-b-2 sm:border-e-2"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {t(section.label)}
                </button>
              ))}
            </nav>

            {/* Content */}
            <div className="min-h-0 min-w-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6 sm:py-5">
              {effectiveActiveSection === "general" && (
                <div className="space-y-4">
                  <FieldRow
                    label={t("web.item.dialog.fieldTitle")}
                    lockIcon={renderLockIcon("title")}
                  >
                    <Input value={form.title} onChange={(e) => setField("title", e.target.value)} />
                  </FieldRow>

                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <FieldRow
                      label={t("web.item.editMetadata.sortTitle")}
                      lockIcon={renderLockIcon("sort_title")}
                    >
                      <Input
                        value={form.sort_title}
                        onChange={(e) => setField("sort_title", e.target.value)}
                      />
                    </FieldRow>
                    <FieldRow
                      label={t("web.item.editMetadata.originalTitle")}
                      lockIcon={renderLockIcon("original_title")}
                    >
                      <Input
                        value={form.original_title}
                        onChange={(e) => setField("original_title", e.target.value)}
                      />
                    </FieldRow>
                  </div>

                  <FieldRow
                    label={t("web.item.editMetadata.overview")}
                    lockIcon={renderLockIcon("overview")}
                  >
                    <textarea
                      value={form.overview}
                      onChange={(e) => setField("overview", e.target.value)}
                      rows={4}
                      className="border-border bg-background text-foreground focus:border-ring focus:ring-ring/50 w-full resize-y rounded-md px-3 py-2.5 text-sm shadow-xs transition-[color,box-shadow] outline-none focus:ring-[3px]"
                    />
                  </FieldRow>

                  <FieldRow
                    label={t("web.item.editMetadata.tagline")}
                    lockIcon={renderLockIcon("tagline")}
                  >
                    <Input
                      value={form.tagline}
                      onChange={(e) => setField("tagline", e.target.value)}
                      placeholder={t("web.item.editMetadata.noTagline")}
                    />
                  </FieldRow>

                  {["movie", "series", "season", "episode"].includes(item.type) && (
                    <MetadataTranslatePanel item={item} />
                  )}

                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    {ownsContentRating && (
                      <FieldRow
                        label={t("web.item.editMetadata.contentRating")}
                        lockIcon={renderLockIcon("content_rating")}
                      >
                        <Input
                          value={form.content_rating}
                          onChange={(e) => setField("content_rating", e.target.value)}
                        />
                      </FieldRow>
                    )}

                    {item.type === "movie" && (
                      <FieldRow
                        label={t("web.item.editMetadata.runtime")}
                        lockIcon={renderLockIcon("runtime")}
                      >
                        <Input
                          type="number"
                          value={form.runtime || ""}
                          onChange={(e) => setField("runtime", parseInt(e.target.value) || 0)}
                        />
                      </FieldRow>
                    )}

                    {item.type === "season" && (
                      <FieldRow label={t("web.item.editMetadata.seasonNumber")}>
                        <Input
                          type="number"
                          value={form.season_number ?? ""}
                          onChange={(e) =>
                            setField("season_number", parseInt(e.target.value) || undefined)
                          }
                        />
                      </FieldRow>
                    )}

                    {item.type === "episode" && (
                      <FieldRow label={t("web.item.editMetadata.seasonNumber")}>
                        <Input
                          type="number"
                          value={form.season_number ?? ""}
                          disabled
                          className="opacity-50"
                        />
                      </FieldRow>
                    )}
                  </div>

                  {item.type === "episode" && (
                    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                      <FieldRow label={t("web.item.editMetadata.episodeNumber")}>
                        <Input
                          type="number"
                          value={form.episode_number ?? ""}
                          onChange={(e) =>
                            setField("episode_number", parseInt(e.target.value) || undefined)
                          }
                        />
                      </FieldRow>
                      <FieldRow
                        label={t("web.item.editMetadata.runtime")}
                        lockIcon={renderLockIcon("runtime")}
                      >
                        <Input
                          type="number"
                          value={form.runtime || ""}
                          onChange={(e) => setField("runtime", parseInt(e.target.value) || 0)}
                        />
                      </FieldRow>
                    </div>
                  )}
                </div>
              )}

              {effectiveActiveSection === "dates" && (
                <div className="space-y-4">
                  {(item.type === "movie" || item.type === "series") && (
                    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                      <FieldRow
                        label={t("web.item.dialog.fieldYear")}
                        lockIcon={renderLockIcon("year")}
                      >
                        <Input
                          type="number"
                          value={form.year || ""}
                          onChange={(e) => setField("year", parseInt(e.target.value) || 0)}
                        />
                      </FieldRow>

                      {item.type === "movie" && (
                        <FieldRow
                          label={t("web.item.editMetadata.releaseDate")}
                          lockIcon={renderLockIcon("release_date")}
                        >
                          <Input
                            type="date"
                            value={form.release_date}
                            onChange={(e) => setField("release_date", e.target.value)}
                          />
                        </FieldRow>
                      )}

                      {item.type === "series" && (
                        <FieldRow
                          label={t("web.item.editMetadata.firstAirDate")}
                          lockIcon={renderLockIcon("first_air_date")}
                        >
                          <Input
                            type="date"
                            value={form.first_air_date}
                            onChange={(e) => setField("first_air_date", e.target.value)}
                          />
                        </FieldRow>
                      )}
                    </div>
                  )}

                  {item.type === "series" && (
                    <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                      <FieldRow
                        label={t("web.item.editMetadata.lastAirDate")}
                        lockIcon={renderLockIcon("last_air_date")}
                      >
                        <Input
                          type="date"
                          value={form.last_air_date}
                          onChange={(e) => setField("last_air_date", e.target.value)}
                        />
                      </FieldRow>
                      <FieldRow
                        label={t("web.item.editMetadata.airTime")}
                        lockIcon={renderLockIcon("air_time")}
                      >
                        <Input
                          type="time"
                          value={form.air_time}
                          onChange={(e) => setField("air_time", e.target.value)}
                        />
                      </FieldRow>
                      <FieldRow
                        label={t("web.item.editMetadata.airTimezone")}
                        lockIcon={renderLockIcon("air_timezone")}
                      >
                        <Input
                          list="air-timezone-options"
                          value={form.air_timezone}
                          placeholder={EXAMPLE_TIMEZONE}
                          onChange={(e) => setField("air_timezone", e.target.value)}
                        />
                        <datalist id="air-timezone-options">
                          {AIR_TIMEZONES.map((timezone) => (
                            <option key={timezone} value={timezone} />
                          ))}
                        </datalist>
                      </FieldRow>
                    </div>
                  )}

                  {(item.type === "season" || item.type === "episode") && (
                    <FieldRow
                      label={t("web.item.editMetadata.airDate")}
                      lockIcon={renderLockIcon("air_date")}
                    >
                      <Input
                        type="date"
                        value={form.air_date}
                        onChange={(e) => setField("air_date", e.target.value)}
                      />
                    </FieldRow>
                  )}

                  {(item.type === "movie" || item.type === "series") && (
                    <>
                      <div className="text-muted-foreground pt-2 text-[11px] font-semibold tracking-wider uppercase">
                        {t("web.item.editMetadata.ratings")}
                      </div>
                      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                        <FieldRow
                          label={t("web.item.editMetadata.imdbRating")}
                          lockIcon={renderLockIcon("rating_imdb")}
                        >
                          <Input
                            type="number"
                            step="0.1"
                            min="0"
                            max="10"
                            value={form.rating_imdb ?? ""}
                            onChange={(e) =>
                              setField(
                                "rating_imdb",
                                e.target.value ? parseFloat(e.target.value) : null,
                              )
                            }
                          />
                        </FieldRow>
                        <FieldRow
                          label={t("web.item.editMetadata.tmdbRating")}
                          lockIcon={renderLockIcon("rating_tmdb")}
                        >
                          <Input
                            type="number"
                            step="0.1"
                            min="0"
                            max="10"
                            value={form.rating_tmdb ?? ""}
                            onChange={(e) =>
                              setField(
                                "rating_tmdb",
                                e.target.value ? parseFloat(e.target.value) : null,
                              )
                            }
                          />
                        </FieldRow>
                        <FieldRow
                          label={t("web.item.editMetadata.rtCritic")}
                          lockIcon={renderLockIcon("rating_rt_critic")}
                        >
                          <Input
                            type="number"
                            min="0"
                            max="100"
                            value={form.rating_rt_critic ?? ""}
                            onChange={(e) =>
                              setField(
                                "rating_rt_critic",
                                e.target.value ? parseInt(e.target.value) : null,
                              )
                            }
                          />
                        </FieldRow>
                        <FieldRow
                          label={t("web.item.editMetadata.rtAudience")}
                          lockIcon={renderLockIcon("rating_rt_audience")}
                        >
                          <Input
                            type="number"
                            min="0"
                            max="100"
                            value={form.rating_rt_audience ?? ""}
                            onChange={(e) =>
                              setField(
                                "rating_rt_audience",
                                e.target.value ? parseInt(e.target.value) : null,
                              )
                            }
                          />
                        </FieldRow>
                      </div>
                    </>
                  )}
                </div>
              )}

              {effectiveActiveSection === "tags" && (
                <div className="space-y-4">
                  <FieldRow
                    label={t("web.item.editMetadata.genres")}
                    lockIcon={renderLockIcon("genres")}
                  >
                    <TagInput
                      value={form.genres}
                      onChange={(v) => setField("genres", v)}
                      placeholder={t("web.item.editMetadata.addGenre")}
                    />
                  </FieldRow>

                  <FieldRow
                    label={t("web.item.editMetadata.studios")}
                    lockIcon={renderLockIcon("studios")}
                  >
                    <TagInput
                      value={form.studios}
                      onChange={(v) => setField("studios", v)}
                      placeholder={t("web.item.editMetadata.addStudio")}
                    />
                  </FieldRow>

                  {item.type === "series" && (
                    <FieldRow
                      label={t("web.item.editMetadata.networks")}
                      lockIcon={renderLockIcon("networks")}
                    >
                      <TagInput
                        value={form.networks}
                        onChange={(v) => setField("networks", v)}
                        placeholder={t("web.item.editMetadata.addNetwork")}
                      />
                    </FieldRow>
                  )}

                  <FieldRow
                    label={t("web.item.editMetadata.countries")}
                    lockIcon={renderLockIcon("countries")}
                  >
                    <TagInput
                      value={form.countries}
                      onChange={(v) => setField("countries", v)}
                      placeholder={t("web.item.editMetadata.addCountry")}
                    />
                  </FieldRow>
                </div>
              )}

              {effectiveActiveSection === "ids" && (
                <div className="space-y-4">
                  <FieldRow label={t("web.item.dialog.imdbId")}>
                    <Input
                      value={form.imdb_id}
                      onChange={(e) => setField("imdb_id", e.target.value)}
                      placeholder={EXAMPLE_IMDB_ID}
                    />
                  </FieldRow>
                  <FieldRow label={t("web.item.dialog.tmdbId")}>
                    <Input
                      value={form.tmdb_id}
                      onChange={(e) => setField("tmdb_id", e.target.value)}
                    />
                  </FieldRow>
                  <FieldRow label={t("web.item.dialog.tvdbId")}>
                    <Input
                      value={form.tvdb_id}
                      onChange={(e) => setField("tvdb_id", e.target.value)}
                    />
                  </FieldRow>
                </div>
              )}

              {canEditImages && (
                <div
                  className={
                    effectiveActiveSection === "images" ? "flex h-full flex-col" : "hidden"
                  }
                >
                  <ImageSelectorTab
                    item={item}
                    enabled={effectiveActiveSection === "images"}
                    onImageApplied={handleImageApplied}
                    onApplyPendingChange={setImageApplyPending}
                  />
                </div>
              )}
            </div>
          </div>

          {/* Footer */}
          <div className="border-border/10 flex shrink-0 flex-col-reverse gap-2 border-t px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5 sm:py-3.5">
            <div>
              {fieldLockMap && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setShowResetConfirm(true)}
                  // A save or image apply still running could lock a field
                  // again after the reset unlocks it.
                  disabled={updateMutation.isPending || imageApplyPending}
                  className="text-muted-foreground hover:text-foreground"
                >
                  {t("web.item.editMetadata.resetToProvider")}
                </Button>
              )}
            </div>
            <div className="flex gap-2 max-sm:w-full">
              <Button
                variant="outline"
                size="sm"
                onClick={() => onOpenChange(false)}
                className="max-sm:flex-1"
              >
                {t("common.cancel")}
              </Button>
              <Button
                size="sm"
                onClick={handleSave}
                disabled={updateMutation.isPending || imageApplyPending}
                className="max-sm:flex-1"
              >
                {updateMutation.isPending
                  ? t("web.item.dialog.saving")
                  : t("web.item.editMetadata.saveChanges")}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={showResetConfirm}
        onOpenChange={setShowResetConfirm}
        title={t("web.item.editMetadata.resetToProvider")}
        description={t(
          RESET_DESCRIPTION_KEYS[item.type] ?? "web.item.editMetadata.resetDescription",
        )}
        confirmLabel={t("web.item.editMetadata.resetConfirm")}
        variant="destructive"
        onConfirm={handleReset}
      />
    </>
  );
}

function FieldRow({
  label,
  lockIcon,
  children,
}: {
  label: string;
  lockIcon?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div>
      <div className="mb-1.5 flex items-center justify-between">
        <Label className="text-muted-foreground text-[12px] font-medium">{label}</Label>
        {lockIcon}
      </div>
      {children}
    </div>
  );
}
