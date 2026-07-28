import { getProfileToken } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import { useUpdateProfile } from "@/hooks/queries/profiles";
import {
  useDeleteSetting,
  useEffectiveSettings,
  useSetDeviceSetting,
  useSetSetting,
  useSetting,
} from "@/hooks/queries/settings";
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
import { LANGUAGE_OPTIONS } from "@/lib/settingsManifest";
import { QUALITY_PRESETS, describeQuality, presetById, presetIdFor } from "@/lib/qualityPresets";
import {
  useClearSettingValue,
  useEffectiveSettings as useEffectiveSettingValues,
  useSetSettingValue,
} from "@/hooks/queries/settingValues";
import { toast } from "sonner";

const AUTO_PLAY_NEXT_KEY = "playback.auto_play_next";

function AutoPlayNextSetting({ profileId }: { profileId: string }) {
  const { data: effective = {} } = useEffectiveSettings(profileId, [AUTO_PLAY_NEXT_KEY]);
  const setDeviceSetting = useSetDeviceSetting();
  const autoplay = effective[AUTO_PLAY_NEXT_KEY]?.effective_value !== "false";

  return (
    <SettingRow
      label="Auto-play next episode"
      description="Start the next episode automatically after the current one ends."
      control={(id) => (
        <Switch
          id={id}
          checked={autoplay}
          disabled={setDeviceSetting.isPending}
          onCheckedChange={(checked) =>
            setDeviceSetting.mutate(
              { key: AUTO_PLAY_NEXT_KEY, value: checked ? "true" : "false" },
              {
                onSuccess: () => toast.success("Auto-play preference saved"),
                onError: () => toast.error("Failed to save auto-play preference"),
              },
            )
          }
        />
      )}
    />
  );
}

function NextUpSetting() {
  const { data: nextUpMode, isLoading } = useSetting("next_up_mode");
  const setSetting = useSetSetting();
  const deleteSetting = useDeleteSetting();
  const currentValue = nextUpMode || "combined";

  return (
    <SettingRow
      label="Next up episodes"
      description="Choose whether upcoming episodes stay with Continue Watching or get their own row."
      control={(id) => (
        <div className="flex items-center gap-2">
          <Select
            value={currentValue}
            onValueChange={(value) =>
              setSetting.mutate(
                { key: "next_up_mode", value },
                { onSuccess: () => toast.success("Next up preference saved") },
              )
            }
            disabled={isLoading || setSetting.isPending}
          >
            <SelectTrigger id={id} className="w-full sm:w-[240px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="combined">With Continue Watching</SelectItem>
              <SelectItem value="separate">Separate row</SelectItem>
            </SelectContent>
          </Select>
          {nextUpMode ? (
            <Button
              variant="ghost"
              size="sm"
              className="h-9 rounded-full px-3"
              onClick={() => deleteSetting.mutate({ key: "next_up_mode" })}
            >
              Reset
            </Button>
          ) : null}
        </div>
      )}
    />
  );
}

/**
 * Quality is two settings behind one picker.
 *
 * The server stores a resolution cap and a bandwidth cap independently, which
 * is what the player has always sent on the wire. Presenting them as one list
 * keeps the choice simple while leaving the two axes free: a preset is a
 * client-side pairing, so retuning what "High" means never needs the server to
 * agree, and someone who sets the axes separately through the API still gets a
 * truthful label rather than a picker showing the wrong entry.
 */
function QualitySetting() {
  const { data: effective } = useEffectiveSettingValues({
    keys: ["playback.preferred_quality", "playback.max_bitrate_kbps"],
  });
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();

  const resolution = effective?.["playback.preferred_quality"]?.value as string | undefined;
  const bitrate = effective?.["playback.max_bitrate_kbps"]?.value as number | null | undefined;
  const selected = presetIdFor(resolution, bitrate);

  const apply = (presetId: string) => {
    const preset = presetById(presetId);
    if (!preset) return;

    setValue.mutate(
      {
        key: "playback.preferred_quality",
        value: preset.resolution,
        identity: { scope: "profile" },
      },
      { onError: () => toast.error("Failed to save video quality") },
    );

    // An uncapped preset clears the bitrate rather than storing a sentinel, so
    // "no cap" stays the absence of a value at every layer.
    if (preset.bitrateKbps === null) {
      clearValue.mutate(
        { key: "playback.max_bitrate_kbps", identity: { scope: "profile" } },
        {
          // Already absent is the desired state, so a 404 is success.
          onError: () => undefined,
        },
      );
    } else {
      setValue.mutate(
        {
          key: "playback.max_bitrate_kbps",
          value: preset.bitrateKbps,
          identity: { scope: "profile" },
        },
        { onError: () => toast.error("Failed to save maximum bitrate") },
      );
    }
  };

  const pending = setValue.isPending || clearValue.isPending;

  return (
    <SettingRow
      label="Video quality"
      description="Choose the quality your profile should request when playback begins."
      control={(id) => (
        <div className="w-full">
          <Select value={selected ?? "__custom"} onValueChange={apply}>
            <SelectTrigger id={id} className="w-full sm:w-[260px]" disabled={pending}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {selected === null && (
                <SelectItem value="__custom" disabled>
                  {describeQuality(resolution, bitrate)}
                </SelectItem>
              )}
              {QUALITY_PRESETS.map((preset) => (
                <SelectItem key={preset.id} value={preset.id}>
                  {preset.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-muted-foreground mt-1.5 text-xs">
            {describeQuality(resolution, bitrate)}
          </p>
        </div>
      )}
    />
  );
}

export default function PlaybackSettings() {
  const { profile, selectProfile } = useAuth();
  const updateMutation = useUpdateProfile();
  const currentProfileToken = getProfileToken() ?? undefined;

  if (!profile) return null;

  const saveProfileField = (body: {
    language?: string;
    preferred_metadata_language?: string;
    auto_skip_intro?: boolean;
    auto_skip_credits?: boolean;
    auto_skip_recap?: boolean;
    auto_play_next_preview?: boolean;
  }) => {
    updateMutation.mutate(
      { id: profile.id, body },
      {
        onSuccess: (updatedProfile) => selectProfile(updatedProfile, currentProfileToken),
        onError: () => toast.error("Failed to save profile setting"),
      },
    );
  };

  return (
    <div className="space-y-6">
      <div className="space-y-4">
        <div className="space-y-3">
          <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Playback</h2>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            Choose the defaults Silo should use when playback starts.
          </p>
        </div>
      </div>

      <SettingsGroup
        title="Defaults"
        description="These preferences apply unless a library or item has a more specific playback choice."
      >
        <QualitySetting />

        <SettingRow
          label="Spoken language"
          description="Prefer a spoken language for this profile when multiple tracks are available."
          control={(id) => (
            <div className="w-full">
              <Select
                value={profile.language || "none"}
                onValueChange={(value) =>
                  saveProfileField({ language: value === "none" ? "" : value })
                }
              >
                <SelectTrigger
                  id={id}
                  className="w-full sm:w-[220px]"
                  disabled={updateMutation.isPending}
                >
                  <SelectValue placeholder="No preference" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">No preference</SelectItem>
                  {LANGUAGE_OPTIONS.filter((language) => language.value).map((language) => (
                    <SelectItem key={language.value} value={language.value}>
                      {language.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
        />

        <SettingRow
          label="Metadata language"
          description="Show titles and descriptions in this language when available. Descriptions can be translated automatically when the server has AI translation enabled."
          control={(id) => (
            <div className="w-full">
              <Select
                value={profile.preferred_metadata_language || "none"}
                onValueChange={(value) =>
                  saveProfileField({
                    preferred_metadata_language: value === "none" ? "" : value,
                  })
                }
              >
                <SelectTrigger
                  id={id}
                  className="w-full sm:w-[220px]"
                  disabled={updateMutation.isPending}
                >
                  <SelectValue placeholder="Library default" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">Library default</SelectItem>
                  {LANGUAGE_OPTIONS.filter((language) => language.value).map((language) => (
                    <SelectItem key={language.value} value={language.value}>
                      {language.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
        />

        <SettingRow
          label="Auto-skip intros"
          description="Jump past intros automatically when Silo can detect them."
          control={(id) => (
            <Switch
              id={id}
              checked={profile.auto_skip_intro ?? false}
              disabled={updateMutation.isPending}
              onCheckedChange={(checked) => saveProfileField({ auto_skip_intro: checked })}
            />
          )}
        />

        <SettingRow
          label="Auto-skip credits"
          description="Move through end credits automatically when a skip is available."
          control={(id) => (
            <Switch
              id={id}
              checked={profile.auto_skip_credits ?? false}
              disabled={updateMutation.isPending}
              onCheckedChange={(checked) => saveProfileField({ auto_skip_credits: checked })}
            />
          )}
        />

        <SettingRow
          label="Auto-skip recaps"
          description="Skip 'previously on…' recaps automatically when Silo can detect them."
          control={(id) => (
            <Switch
              id={id}
              checked={profile.auto_skip_recap ?? false}
              disabled={updateMutation.isPending}
              onCheckedChange={(checked) => saveProfileField({ auto_skip_recap: checked })}
            />
          )}
        />

        <SettingRow
          label="Start next at preview"
          description="Begin the next episode when the current one reaches its next-episode preview teaser, rather than waiting for the end credits."
          control={(id) => (
            <Switch
              id={id}
              checked={profile.auto_play_next_preview ?? false}
              disabled={updateMutation.isPending}
              onCheckedChange={(checked) => saveProfileField({ auto_play_next_preview: checked })}
            />
          )}
        />

        <AutoPlayNextSetting profileId={profile.id} />

        <NextUpSetting />
      </SettingsGroup>
    </div>
  );
}
