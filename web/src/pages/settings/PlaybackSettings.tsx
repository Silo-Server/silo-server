import { useMemo, useState } from "react";
import { PageHeader } from "@/components/page/PageHeader";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { SettingRow } from "@/components/settings/SettingRow";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { optionsFor } from "@/lib/settingsDisplay";
import { settingDescription, settingLabel } from "@/lib/settingsText";
import { SETTING_DEFINITIONS, SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { bitrateSelectChoices } from "@/lib/bitrateOptions";
import {
  settingsCapabilitiesSupportKey,
  useEffectiveSettings,
  useSettingsCapabilities,
} from "@/hooks/queries/settingValues";
import { useAutoPlayNextSetting } from "@/hooks/queries/autoPlayNext";
import { useSeekPreferences } from "@/hooks/queries/seekPreferences";
import type { SeekDirection } from "@/lib/seekIntervals";
import { useProfileDefaultWriter } from "@/hooks/queries/profileDefaults";
import { isBrowserDecodingEnabled, setBrowserDecodingEnabled } from "@/player/engine/settings";
import { normalizeQualityPreferenceV3 } from "@/player/protocol-v3";
import { toast } from "sonner";
import { LearnedLimitRow, PlaybackConnection } from "./PlaybackConnection";

/**
 * Every preference on this screen is the profile's own choice. A device, a
 * library, or a series can override several of them, and the contract resolves
 * those above the profile row — so useProfileDefaultWriter writes the profile
 * layer and clears any device override that would otherwise keep shadowing it.
 */
/**
 * Read in one batch: the screen wants every key at once.
 *
 * playback.auto_play_next is deliberately absent — it is read and written
 * through useAutoPlayNextSetting, shared with the post-roll toggle so the two
 * surfaces cannot land on different scopes.
 */
const BASE_PLAYBACK_KEYS: SettingKey[] = [
  SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW,
  SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS,
  SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP,
];

const BUFFERING_MODE_SETTING = SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_BUFFERING_QUALITY_MODE];

// Radix Select cannot represent "" as an item value, so the unset entry needs
// a sentinel that never collides with a stored bitrate.
const NO_BITRATE_LIMIT = "__no_limit__";

/**
 * Quality is the same two settings every other surface edits.
 *
 * The server stores a resolution cap and a bandwidth cap independently, and
 * the device screen already presents them as two controls. Offering the same
 * two rows here means a device override reads as an override of something the
 * user can see, rather than of half a compound preset — and combinations no
 * preset covered ("Auto but capped at 40 Mbps" for a remote box) become
 * expressible instead of rendering as a disabled "custom" entry.
 *
 * A stored "original" is shown as Auto: the contract keeps it as a deprecated
 * value that means the same thing, and no longer offers it.
 */
function QualitySetting() {
  const t = useT();
  const { data: effective } = useEffectiveSettings({
    keys: [SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY, SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS],
  });
  const { save, reset, isSaving } = useProfileDefaultWriter(effective);

  const qualityDefinition = SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY];
  const bitrateDefinition = SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS];

  const resolution = normalizeQualityPreferenceV3(
    (effective?.[SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]?.value ??
      qualityDefinition.defaultValue) as string,
  );
  const bitrate = effective?.[SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS]?.value as
    | number
    | null
    | undefined;
  const bitrateValue = bitrate == null ? "" : String(bitrate);
  const bitrateChoices = bitrateSelectChoices(
    bitrateDefinition,
    bitrateValue,
    t("web.settings.value.noLimit"),
  );

  return (
    <>
      <SettingRow
        label={settingLabel(qualityDefinition)}
        description={
          resolution === "auto"
            ? t("web.settings.playback.quality.autoDescription")
            : t("web.settings.playback.quality.description")
        }
        control={(id) => (
          <Select
            value={resolution}
            disabled={isSaving}
            onValueChange={(value) =>
              save(SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY, value).catch(() =>
                toast.error(t("web.settings.playback.quality.saveFailed")),
              )
            }
          >
            <SelectTrigger id={id} className="w-full sm:w-[220px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {optionsFor(qualityDefinition).map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      />

      <SettingRow
        label={settingLabel(bitrateDefinition)}
        description={t("web.settings.playback.maxBitrate.description")}
        control={(id) => (
          <Select
            value={bitrateValue === "" ? NO_BITRATE_LIMIT : bitrateValue}
            disabled={isSaving}
            onValueChange={(next) => {
              // "No limit" clears the rows rather than storing a sentinel, so
              // "no cap" stays the absence of a value at every layer. The reset
              // clears the device row too — otherwise an override would keep
              // capping playback after this control said it did not.
              const request =
                next === NO_BITRATE_LIMIT
                  ? reset(SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS)
                  : save(SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS, Number(next));
              request.catch(() => toast.error(t("web.settings.playback.maxBitrate.saveFailed")));
            }}
          >
            <SelectTrigger id={id} className="w-full sm:w-[220px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {bitrateChoices.map((choice) => (
                <SelectItem
                  key={choice.value || NO_BITRATE_LIMIT}
                  value={choice.value === "" ? NO_BITRATE_LIMIT : choice.value}
                >
                  {choice.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      />
    </>
  );
}

/**
 * Auto-play shares its hook with the post-roll toggle.
 *
 * Both surfaces render the resolved value, and the contract resolves
 * profile_device above profile — so if this screen wrote the profile row while
 * the player wrote the device row, this switch would save a value the device
 * row keeps shadowing and snap straight back. The shared hook writes the
 * profile and clears any device row, which is also the only way a migrated
 * per-device override becomes reachable from the web UI.
 */
function AutoPlayNextSetting() {
  const t = useT();
  const { enabled, setEnabled, isSaving } = useAutoPlayNextSetting();

  return (
    <SettingRow
      label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT])}
      description={t("web.settings.playback.autoPlayNext.description")}
      control={(id) => (
        <Switch
          id={id}
          checked={enabled}
          disabled={isSaving}
          onCheckedChange={(checked) => {
            setEnabled(checked).catch(() => toast.error(t("web.settings.playback.saveFailed")));
          }}
        />
      )}
    />
  );
}

const SEEK_DIRECTION_LABELS: Record<SeekDirection, MessageKey> = {
  back: "web.settings.playback.seek.rewindInterval",
  forward: "web.settings.playback.seek.forwardInterval",
};

/** Per media kind and direction: the row description, its load error and its save error. */
const SEEK_ROW_TEXT: Record<
  "video" | "audiobook",
  Record<SeekDirection, { description: MessageKey; loadFailed: MessageKey; saveFailed: MessageKey }>
> = {
  video: {
    back: {
      description: "web.settings.playback.seek.video.rewindDescription",
      loadFailed: "web.settings.playback.seek.video.rewindLoadFailed",
      saveFailed: "web.settings.playback.seek.video.rewindSaveFailed",
    },
    forward: {
      description: "web.settings.playback.seek.video.forwardDescription",
      loadFailed: "web.settings.playback.seek.video.forwardLoadFailed",
      saveFailed: "web.settings.playback.seek.video.forwardSaveFailed",
    },
  },
  audiobook: {
    back: {
      description: "web.settings.playback.seek.audiobook.rewindDescription",
      loadFailed: "web.settings.playback.seek.audiobook.rewindLoadFailed",
      saveFailed: "web.settings.playback.seek.audiobook.rewindSaveFailed",
    },
    forward: {
      description: "web.settings.playback.seek.audiobook.forwardDescription",
      loadFailed: "web.settings.playback.seek.audiobook.forwardLoadFailed",
      saveFailed: "web.settings.playback.seek.audiobook.forwardSaveFailed",
    },
  },
};

function formatSeconds(t: Translate, seconds: number) {
  return t("web.settings.playback.seek.seconds", { count: seconds, count_text: String(seconds) });
}

/**
 * One rewind/fast-forward pair for a media kind. The Select is controlled by
 * the resolved value, so a rejected write leaves the previous choice selected
 * and the toast is the only trace of the attempt — never a value the server
 * did not accept.
 */
function SeekIntervalRows({
  media,
  prefs,
}: {
  media: "video" | "audiobook";
  prefs: ReturnType<typeof useSeekPreferences>;
}) {
  const t = useT();
  return (
    <>
      {(["back", "forward"] as const).map((direction) => {
        const value = direction === "back" ? prefs.skipBack : prefs.skipForward;
        const text = SEEK_ROW_TEXT[media][direction];
        const choices = prefs.choices[direction];
        return (
          <SettingRow
            key={direction}
            label={t(SEEK_DIRECTION_LABELS[direction])}
            description={prefs.error ? t(text.loadFailed) : t(text.description)}
            control={(id) => (
              <Select
                value={prefs.isLoading || prefs.error ? undefined : String(value)}
                disabled={prefs.isLoading || Boolean(prefs.error) || prefs.isSaving}
                onValueChange={(next) => {
                  prefs.save(direction, Number(next)).catch(() => toast.error(t(text.saveFailed)));
                }}
              >
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue
                    placeholder={
                      prefs.isLoading
                        ? t("web.settings.playback.seek.loading")
                        : t("web.settings.playback.unavailable")
                    }
                  />
                </SelectTrigger>
                <SelectContent>
                  {choices.map((seconds) => (
                    <SelectItem key={seconds} value={String(seconds)}>
                      {formatSeconds(t, seconds)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        );
      })}
    </>
  );
}

type ImportOutcome = { saved: SeekDirection[]; failed: SeekDirection[] };

/** The `select` value naming one or both directions in the import messages. */
function directionsArg(directions: SeekDirection[]) {
  return directions.length > 1 ? "both" : directions[0] === "back" ? "back" : "forward";
}

/**
 * Explicit, click-driven import of the intervals this browser stored before
 * the server learned to keep them per profile. Nothing imports on mount or on
 * a profile switch, the legacy values stay in local storage after any outcome
 * so a failed direction can be retried, and the row stays available because
 * it is an overwrite the user asks for, not a migration to be marked done.
 */
function LegacyAudiobookImportRow({ prefs }: { prefs: ReturnType<typeof useSeekPreferences> }) {
  const t = useT();
  const [outcome, setOutcome] = useState<ImportOutcome | null>(null);
  const { back, forward } = prefs.legacy;
  if (back === undefined && forward === undefined) return null;

  const body =
    back !== undefined && forward !== undefined
      ? t("web.settings.playback.legacy.bodyBoth", {
          back: formatSeconds(t, back),
          forward: formatSeconds(t, forward),
        })
      : back !== undefined
        ? t("web.settings.playback.legacy.bodyBack", { back: formatSeconds(t, back) })
        : t("web.settings.playback.legacy.bodyForward", { forward: formatSeconds(t, forward!) });

  const runImport = async () => {
    setOutcome(null);
    const results = await prefs.importLegacy();
    const next: ImportOutcome = { saved: [], failed: [] };
    for (const result of results) {
      (result.status === "fulfilled" ? next.saved : next.failed).push(result.direction);
    }
    setOutcome(next);
    if (next.failed.length === 0) {
      toast.success(
        t("web.settings.playback.legacy.imported", { directions: directionsArg(next.saved) }),
      );
    } else if (next.saved.length === 0) {
      toast.error(
        t("web.settings.playback.legacy.importFailed", { directions: directionsArg(next.failed) }),
      );
    } else {
      toast.error(
        t("web.settings.playback.legacy.importPartial", { saved: directionsArg(next.saved) }),
      );
    }
  };

  return (
    <div className="border-border/50 grid gap-3 border-t pt-4 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
      <div className="min-w-0 space-y-1">
        <p className="text-sm font-medium">{t("web.settings.playback.legacy.title")}</p>
        <p className="text-muted-foreground text-[13px] leading-relaxed">{body}</p>
        {outcome ? (
          <p
            className={
              outcome.failed.length > 0
                ? "text-destructive text-[13px] leading-relaxed"
                : "text-muted-foreground text-[13px] leading-relaxed"
            }
            role="status"
          >
            {[
              outcome.saved.length > 0 &&
                t("web.settings.playback.legacy.saved", {
                  directions: directionsArg(outcome.saved),
                }),
              outcome.failed.length > 0 &&
                t("web.settings.playback.legacy.notSaved", {
                  directions: directionsArg(outcome.failed),
                }),
            ]
              .filter(Boolean)
              .join(" ")}
          </p>
        ) : null}
      </div>
      <div className="flex md:justify-end">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="rounded-full"
          disabled={prefs.isSaving || prefs.isLoading || Boolean(prefs.error)}
          onClick={() => void runImport()}
        >
          {t("web.settings.playback.legacy.button")}
        </Button>
      </div>
    </div>
  );
}

/**
 * Seek intervals belong to the household profile, not this device: the
 * server stores all four per profile so every supported client jumps by the
 * same amount. Older servers get no shared controls at all — the audiobook
 * player keeps its browser-local choice there — and an unanswered capability
 * check renders nothing writable rather than a guess.
 */
function SeekControls() {
  const t = useT();
  const video = useSeekPreferences("video");
  const audiobook = useSeekPreferences("audiobook");
  const supported = video.supported && audiobook.supported;
  const discoveryError = !supported && !video.isLoading ? video.error : null;

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h3 className="text-base font-semibold tracking-tight">
          {t("web.settings.playback.seek.title")}
        </h3>
        <p className="text-muted-foreground max-w-2xl text-[13px] leading-relaxed">
          {t("web.settings.playback.seek.description")}
        </p>
      </div>

      {supported || video.isLoading ? (
        <>
          <SettingsGroup
            title={t("web.settings.playback.seek.videoTitle")}
            description={t("web.settings.playback.seek.videoDescription")}
          >
            <SeekIntervalRows media="video" prefs={video} />
          </SettingsGroup>
          <SettingsGroup
            title={t("web.settings.playback.seek.audiobookTitle")}
            description={t("web.settings.playback.seek.audiobookDescription")}
          >
            <SeekIntervalRows media="audiobook" prefs={audiobook} />
            {supported ? <LegacyAudiobookImportRow prefs={audiobook} /> : null}
          </SettingsGroup>
        </>
      ) : discoveryError ? (
        <p className="text-destructive text-sm" role="alert">
          {t("web.settings.playback.seek.discoveryFailed")}
        </p>
      ) : (
        <p className="text-muted-foreground text-sm">
          {t("web.settings.playback.seek.unsupported")}
        </p>
      )}
    </div>
  );
}

export default function PlaybackSettings() {
  const t = useT();
  const capabilities = useSettingsCapabilities();
  const supportsIntroSkipMode = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE,
  );
  /**
   * Which intro control this server can honestly show.
   *
   * The legacy switch is not a safe default while the answer is unknown. On a
   * revision-7 server a profile set to `never` is mirrored into the deprecated
   * boolean as false; touching the switch would write that key, and the
   * server's mirror would turn a deliberate `never` into `ask` or `always`
   * with no way back. So the fallback is only reached once a successful
   * capabilities response has proved the enum is genuinely unavailable — a
   * failed request means unknown, not old.
   */
  const introSkipControl: "mode" | "legacy" | "unknown" = supportsIntroSkipMode
    ? "mode"
    : capabilities.isSuccess
      ? "legacy"
      : "unknown";
  const supportsBuffering = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.PLAYBACK_BUFFERING_QUALITY_MODE,
  );
  // The two provenance keys arrived together, so one gate covers both. Rows for
  // keys the server does not define are left out rather than rendered dead: a
  // switch that cannot save is worse than no switch.
  const supportsMarkerProvenance = settingsCapabilitiesSupportKey(
    capabilities.data,
    SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE,
  );
  const playbackKeys = useMemo(
    () => [
      ...BASE_PLAYBACK_KEYS,
      ...(supportsBuffering ? [SETTING_KEYS.PLAYBACK_BUFFERING_QUALITY_MODE] : []),
      ...(supportsMarkerProvenance
        ? [SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE, SETTING_KEYS.PLAYBACK_SHOW_MARKER_SOURCE]
        : []),
      supportsIntroSkipMode
        ? SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE
        : SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
    ],
    [supportsIntroSkipMode, supportsBuffering, supportsMarkerProvenance],
  );
  const { data: effective } = useEffectiveSettings({ keys: playbackKeys });
  const { save: saveProfileDefault, isSaving } = useProfileDefaultWriter(effective);

  // The effective endpoint resolves an unset key to the contract default, so
  // every control below reads its value from the same answer rather than from
  // a local literal that could disagree with the server about "unset".
  const read = <T,>(key: SettingKey) =>
    (effective?.[key]?.value ?? SETTING_DEFINITIONS[key].defaultValue) as T;
  const ignoresLearnedLimit =
    read<string>(SETTING_KEYS.PLAYBACK_BUFFERING_QUALITY_MODE) === "never";

  // Saves at profile scope and clears any device override shadowing it —
  // several of these keys are device-overridable, and without the clear the
  // control would snap back to the override it cannot see.
  const saveValue = (key: SettingKey, value: unknown) => {
    saveProfileDefault(key, value).catch(() => toast.error(t("web.settings.playback.saveFailed")));
  };

  const pending = isSaving;
  const introSkipModes = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE]);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.playback.title")}
        subtitle={t("web.settings.playback.subtitle")}
      />

      <SettingsGroup
        title={t("web.settings.playback.quality.title")}
        description={t("web.settings.playback.quality.groupDescription")}
      >
        <QualitySetting />
        {supportsBuffering && (
          <SettingRow
            label={settingLabel(BUFFERING_MODE_SETTING)}
            description={settingDescription(BUFFERING_MODE_SETTING)}
            control={(id) => (
              <Select
                value={read<string>(SETTING_KEYS.PLAYBACK_BUFFERING_QUALITY_MODE)}
                disabled={pending}
                onValueChange={(value) =>
                  saveValue(SETTING_KEYS.PLAYBACK_BUFFERING_QUALITY_MODE, value)
                }
              >
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {optionsFor(BUFFERING_MODE_SETTING).map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        )}
        {/* Never sends learned_limit: "ignore" on every start, so no limit applies. */}
        {supportsBuffering && !ignoresLearnedLimit && <LearnedLimitRow />}
      </SettingsGroup>
      {supportsBuffering && <PlaybackConnection ignoresLearnedLimit={ignoresLearnedLimit} />}

      <SettingsGroup title={t("web.settings.playback.skipping.title")}>
        {introSkipControl === "mode" ? (
          <SettingRow
            label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE])}
            description={t("web.settings.playback.skipping.introModeDescription")}
            control={(id) => (
              <Select
                value={read<string>(SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE)}
                disabled={pending}
                onValueChange={(value) => saveValue(SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE, value)}
              >
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {introSkipModes.map((mode) => (
                    <SelectItem key={mode.value} value={mode.value}>
                      {mode.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        ) : introSkipControl === "unknown" ? (
          // Neither control can be rendered truthfully yet, so the row keeps
          // its place and asserts no value at all rather than showing a switch
          // whose position would be a guess the user could act on.
          <SettingRow
            label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE])}
            description={t("web.settings.playback.skipping.introModeUnknown")}
            control={(id) => (
              <Select disabled>
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue placeholder={t("web.settings.playback.unavailable")} />
                </SelectTrigger>
                <SelectContent>
                  {introSkipModes.map((mode) => (
                    <SelectItem key={mode.value} value={mode.value}>
                      {mode.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        ) : (
          <SettingRow
            label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO])}
            description={settingDescription(
              SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO],
            )}
            control={(id) => (
              <Switch
                id={id}
                checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO)}
                disabled={pending}
                onCheckedChange={(checked) =>
                  saveValue(SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO, checked)
                }
              />
            )}
          />
        )}

        {supportsMarkerProvenance && (
          <SettingRow
            label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE])}
            description={settingDescription(
              SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE],
            )}
            control={(id) => (
              <Switch
                id={id}
                checked={read<boolean>(SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE)}
                disabled={pending}
                onCheckedChange={(checked) =>
                  saveValue(SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE, checked)
                }
              />
            )}
          />
        )}

        {supportsMarkerProvenance && (
          <SettingRow
            label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_SOURCE])}
            description={settingDescription(
              SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_SOURCE],
            )}
            control={(id) => (
              <Switch
                id={id}
                checked={read<boolean>(SETTING_KEYS.PLAYBACK_SHOW_MARKER_SOURCE)}
                disabled={pending}
                onCheckedChange={(checked) =>
                  saveValue(SETTING_KEYS.PLAYBACK_SHOW_MARKER_SOURCE, checked)
                }
              />
            )}
          />
        )}

        <SettingRow
          label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP])}
          description={t("web.settings.playback.skipping.recapDescription")}
          control={(id) => (
            <Switch
              id={id}
              checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP)}
              disabled={pending}
              onCheckedChange={(checked) =>
                saveValue(SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP, checked)
              }
            />
          )}
        />

        <SettingRow
          label={settingLabel(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS])}
          description={settingDescription(
            SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS],
          )}
          control={(id) => (
            <Switch
              id={id}
              checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS)}
              disabled={pending}
              onCheckedChange={(checked) =>
                saveValue(SETTING_KEYS.PLAYBACK_AUTO_SKIP_CREDITS, checked)
              }
            />
          )}
        />
      </SettingsGroup>

      {/* Sits with the marker settings above: these are the same feature seen
          from the reporting side, and the viewer's own key lives here rather
          than on the admin plugin page, which is server-wide. */}

      <SettingsGroup title={t("web.settings.playback.nextEpisode.title")}>
        <SettingRow
          label={t("web.settings.playback.nextEpisode.previewLabel")}
          description={t("web.settings.playback.nextEpisode.previewDescription")}
          control={(id) => (
            <Switch
              id={id}
              checked={read<boolean>(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW)}
              disabled={pending}
              onCheckedChange={(checked) =>
                saveValue(SETTING_KEYS.PLAYBACK_AUTO_PLAY_NEXT_PREVIEW, checked)
              }
            />
          )}
        />

        <AutoPlayNextSetting />
      </SettingsGroup>

      <SeekControls />

      <BrowserDecodingSetting />
    </div>
  );
}

/**
 * Stored in this browser only: whether it can decode a file depends on the
 * browser and the GPU, not on the profile.
 */
function BrowserDecodingSetting() {
  const t = useT();
  const [enabled, setEnabled] = useState(isBrowserDecodingEnabled);
  return (
    <SettingsGroup
      title={t("web.settings.playback.browser.title")}
      description={t("web.settings.playback.browser.description")}
    >
      <SettingRow
        label={t("web.settings.playback.browser.decodeLabel")}
        description={t("web.settings.playback.browser.decodeDescription")}
        control={(id) => (
          <Switch
            id={id}
            checked={enabled}
            onCheckedChange={(checked) => {
              setBrowserDecodingEnabled(checked);
              setEnabled(checked);
            }}
          />
        )}
      />
    </SettingsGroup>
  );
}
