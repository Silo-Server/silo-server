import { useMemo, useState } from "react";
import { PageHeader } from "@/components/page/PageHeader";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { TheIntroDbReportSettings } from "@/components/settings/TheIntroDbReportSettings";
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
import { optionsFor } from "@/lib/settingsDisplay";
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

const INTRO_SKIP_MODES = optionsFor(SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_INTRO_SKIP_MODE]);
const BUFFERING_MODE_SETTING = SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_BUFFERING_QUALITY_MODE];

// Radix Select cannot represent "" as an item value, so the unset entry needs
// a sentinel that never collides with a stored bitrate.
const NO_BITRATE_LIMIT = "__no_limit__";

const AUTO_QUALITY_DESCRIPTION =
  "Plays the original file whenever your device and connection can handle it.";

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
  const bitrateChoices = bitrateSelectChoices(bitrateDefinition, bitrateValue, "No limit");

  return (
    <>
      <SettingRow
        label="Preferred quality"
        description={
          resolution === "auto"
            ? AUTO_QUALITY_DESCRIPTION
            : "The resolution your profile should request when playback begins."
        }
        control={(id) => (
          <Select
            value={resolution}
            disabled={isSaving}
            onValueChange={(value) =>
              save(SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY, value).catch(() =>
                toast.error("Failed to save preferred quality"),
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
        label="Maximum bitrate"
        description="Cap how much bandwidth playback may use. No limit means Silo picks for the chosen resolution."
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
              request.catch(() => toast.error("Failed to save maximum bitrate"));
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
  const { enabled, setEnabled, isSaving } = useAutoPlayNextSetting();

  return (
    <SettingRow
      label="Auto-play next episode"
      description="Start the next episode automatically after the current one ends."
      control={(id) => (
        <Switch
          id={id}
          checked={enabled}
          disabled={isSaving}
          onCheckedChange={(checked) => {
            setEnabled(checked).catch(() => toast.error("Failed to save playback setting"));
          }}
        />
      )}
    />
  );
}

const SEEK_DIRECTION_LABELS: Record<SeekDirection, string> = {
  back: "Rewind interval",
  forward: "Fast-forward interval",
};

function formatSeconds(seconds: number) {
  return `${seconds} seconds`;
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
  const noun = media === "video" ? "video" : "audiobooks";
  return (
    <>
      {(["back", "forward"] as const).map((direction) => {
        const value = direction === "back" ? prefs.skipBack : prefs.skipForward;
        const verb = direction === "back" ? "rewind" : "fast-forward";
        const choices = prefs.choices[direction];
        return (
          <SettingRow
            key={direction}
            label={SEEK_DIRECTION_LABELS[direction]}
            description={
              prefs.error
                ? `Could not load the ${verb} interval for ${noun}.`
                : `How far ${noun === "video" ? "video" : "an audiobook"} jumps when you ${verb}.`
            }
            control={(id) => (
              <Select
                value={prefs.isLoading || prefs.error ? undefined : String(value)}
                disabled={prefs.isLoading || Boolean(prefs.error) || prefs.isSaving}
                onValueChange={(next) => {
                  prefs
                    .save(direction, Number(next))
                    .catch(() => toast.error(`Failed to save ${verb} interval for ${noun}`));
                }}
              >
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue placeholder={prefs.isLoading ? "Loading…" : "Unavailable"} />
                </SelectTrigger>
                <SelectContent>
                  {choices.map((seconds) => (
                    <SelectItem key={seconds} value={String(seconds)}>
                      {formatSeconds(seconds)}
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

function describeDirections(directions: SeekDirection[]) {
  return directions
    .map((direction) => (direction === "back" ? "rewind" : "fast-forward"))
    .join(" and ");
}

/**
 * Explicit, click-driven import of the intervals this browser stored before
 * the server learned to keep them per profile. Nothing imports on mount or on
 * a profile switch, the legacy values stay in local storage after any outcome
 * so a failed direction can be retried, and the row stays available because
 * it is an overwrite the user asks for, not a migration to be marked done.
 */
function LegacyAudiobookImportRow({ prefs }: { prefs: ReturnType<typeof useSeekPreferences> }) {
  const [outcome, setOutcome] = useState<ImportOutcome | null>(null);
  const entries = (Object.entries(prefs.legacy) as [SeekDirection, number][]).sort(([a]) =>
    a === "back" ? -1 : 1,
  );
  if (entries.length === 0) return null;

  const listed = entries
    .map(([direction, seconds]) =>
      direction === "back"
        ? `rewind ${formatSeconds(seconds)}`
        : `fast-forward ${formatSeconds(seconds)}`,
    )
    .join(" and ");
  const untouched = entries.length === 1 ? (entries[0]![0] === "back" ? "forward" : "back") : null;

  const runImport = async () => {
    setOutcome(null);
    const results = await prefs.importLegacy();
    const next: ImportOutcome = { saved: [], failed: [] };
    for (const result of results) {
      (result.status === "fulfilled" ? next.saved : next.failed).push(result.direction);
    }
    setOutcome(next);
    if (next.failed.length === 0) {
      toast.success(`Imported ${describeDirections(next.saved)} interval for audiobooks`);
    } else if (next.saved.length === 0) {
      toast.error(`Failed to import ${describeDirections(next.failed)} interval for audiobooks`);
    } else {
      toast.error(
        `Imported ${describeDirections(next.saved)} interval, but ${describeDirections(next.failed)} failed`,
      );
    }
  };

  return (
    <div className="border-border/50 grid gap-3 border-t pt-4 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
      <div className="min-w-0 space-y-1">
        <p className="text-sm font-medium">Use this browser's audiobook intervals</p>
        <p className="text-muted-foreground text-[13px] leading-relaxed">
          This browser still has {listed} saved locally. Importing replaces{" "}
          {entries.length === 1 ? "that interval" : "both intervals"} for the active profile across
          supported apps
          {untouched
            ? `; the ${untouched === "back" ? "rewind" : "fast-forward"} interval stays as it is`
            : ""}
          .
        </p>
        {outcome ? (
          <p
            className={
              outcome.failed.length > 0
                ? "text-destructive text-[13px] leading-relaxed"
                : "text-muted-foreground text-[13px] leading-relaxed"
            }
            role="status"
          >
            {outcome.saved.length > 0 ? `Saved ${describeDirections(outcome.saved)}.` : null}
            {outcome.saved.length > 0 && outcome.failed.length > 0 ? " " : null}
            {outcome.failed.length > 0
              ? `Could not save ${describeDirections(outcome.failed)}. Try again to retry.`
              : null}
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
          Use this browser's audiobook intervals
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
  const video = useSeekPreferences("video");
  const audiobook = useSeekPreferences("audiobook");
  const supported = video.supported && audiobook.supported;
  const discoveryError = !supported && !video.isLoading ? video.error : null;

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h3 className="text-base font-semibold tracking-tight">Seek controls</h3>
        <p className="text-muted-foreground max-w-2xl text-[13px] leading-relaxed">
          How far rewind and fast-forward jump. These belong to the active profile and follow it
          across supported Silo apps.
        </p>
      </div>

      {supported || video.isLoading ? (
        <>
          <SettingsGroup
            title="Video"
            description="Applies to the transport buttons, double-tap gestures, arrow keys, and system media controls."
          >
            <SeekIntervalRows media="video" prefs={video} />
          </SettingsGroup>
          <SettingsGroup
            title="Audiobooks"
            description="The same values the audiobook player's settings menu shows and edits."
          >
            <SeekIntervalRows media="audiobook" prefs={audiobook} />
            {supported ? <LegacyAudiobookImportRow prefs={audiobook} /> : null}
          </SettingsGroup>
        </>
      ) : discoveryError ? (
        <p className="text-destructive text-sm" role="alert">
          Could not check whether this server supports shared seek intervals. Reload to try again.
        </p>
      ) : (
        <p className="text-muted-foreground text-sm">
          This server does not store seek intervals per profile yet. Audiobook intervals can still
          be set for this browser from the audiobook player.
        </p>
      )}
    </div>
  );
}

export default function PlaybackSettings() {
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
    saveProfileDefault(key, value).catch(() => toast.error("Failed to save playback setting"));
  };

  const pending = isSaving;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Playback"
        subtitle="The defaults this profile starts playback with. A library, a series, or one of your devices can override some of them."
      />

      <SettingsGroup
        title="Quality"
        description="Each of your devices can override these, and a household manager can cap them for this profile."
      >
        <QualitySetting />
        {supportsBuffering && (
          <SettingRow
            label={BUFFERING_MODE_SETTING.label}
            description={BUFFERING_MODE_SETTING.description}
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

      <SettingsGroup title="Skipping">
        {introSkipControl === "mode" ? (
          <SettingRow
            label="Skip intros"
            description="Leave intros alone, offer a Skip Intro button, or skip automatically with an undo."
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
                  {INTRO_SKIP_MODES.map((mode) => (
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
            label="Skip intros"
            description="Available once Silo has checked what this server supports."
            control={(id) => (
              <Select disabled>
                <SelectTrigger id={id} className="w-full sm:w-[220px]">
                  <SelectValue placeholder="Unavailable" />
                </SelectTrigger>
                <SelectContent>
                  {INTRO_SKIP_MODES.map((mode) => (
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
            label="Auto-skip intros"
            description="Jump past intros automatically when Silo can detect them."
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
            label={SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE].label}
            description={
              SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_CONFIDENCE].description
            }
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
            label={SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_SOURCE].label}
            description={SETTING_DEFINITIONS[SETTING_KEYS.PLAYBACK_SHOW_MARKER_SOURCE].description}
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
          label="Auto-skip recaps"
          description="Skip 'previously on…' recaps automatically when Silo can detect them."
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
          label="Auto-skip credits"
          description="Move through end credits automatically when a skip is available."
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
      <TheIntroDbReportSettings />

      <SettingsGroup title="Next episode">
        <SettingRow
          label="Start next at preview"
          description="Begin the next episode when the current one reaches its next-episode preview teaser, rather than waiting for the end credits."
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
  const [enabled, setEnabled] = useState(isBrowserDecodingEnabled);
  return (
    <SettingsGroup
      title="This browser"
      description="Saved in this browser only. Other browsers and apps keep their own choice."
    >
      <SettingRow
        label="Decode in the browser (beta)"
        description="Play files this browser cannot play natively, such as Dolby TrueHD or DTS audio and HDR video on SDR screens, by decoding them in the browser instead of converting them on the server. Applies from the next video you start."
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
