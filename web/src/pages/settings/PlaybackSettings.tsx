import { useId, type ReactNode } from "react";

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
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { LANGUAGE_OPTIONS } from "@/lib/settingsManifest";
import { toast } from "sonner";

const AUTO_PLAY_NEXT_KEY = "playback.auto_play_next";

interface SettingRowProps {
  label: string;
  description: string;
  control: (id: string) => ReactNode;
}

function SettingRow({ label, description, control }: SettingRowProps) {
  const id = useId();

  return (
    <div className="border-border/50 grid gap-3 border-t pt-4 first:border-t-0 first:pt-0 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
      <div className="min-w-0 space-y-1">
        <Label htmlFor={id} className="text-sm font-medium">
          {label}
        </Label>
        <p className="text-muted-foreground text-[13px] leading-relaxed">{description}</p>
      </div>
      <div className="flex md:justify-end">{control(id)}</div>
    </div>
  );
}

function AutoPlayNextSetting({ profileId }: { profileId: string }) {
  const { data: effective = {} } = useEffectiveSettings(profileId, [AUTO_PLAY_NEXT_KEY]);
  const setDeviceSetting = useSetDeviceSetting();
  const autoplay = effective[AUTO_PLAY_NEXT_KEY]?.effective_value !== "false";

  return (
    <SettingRow
      label="Auto-play next episode"
      description="Start the next episode automatically after the current one ends."
      control={(id) => (
        <div id={id}>
          <Switch
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
        </div>
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
        <div id={id} className="flex items-center gap-2">
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
            <SelectTrigger className="w-full sm:w-[240px]">
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

export default function PlaybackSettings() {
  const { profile, selectProfile } = useAuth();
  const updateMutation = useUpdateProfile();
  const currentProfileToken = getProfileToken() ?? undefined;

  if (!profile) return null;

  const qualityPreference =
    profile.quality_preference?.toLowerCase() === "4k"
      ? "2160p"
      : profile.quality_preference || "auto";

  const saveProfileField = (body: {
    quality_preference?: string;
    language?: string;
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
        <SettingRow
          label="Video quality"
          description="Choose the quality your profile should request when playback begins."
          control={(id) => (
            <div id={id} className="w-full">
              <Select
                value={qualityPreference}
                onValueChange={(value) => saveProfileField({ quality_preference: value })}
              >
                <SelectTrigger className="w-full sm:w-[220px]" disabled={updateMutation.isPending}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">Auto</SelectItem>
                  <SelectItem value="original">Original</SelectItem>
                  <SelectItem value="2160p">4K</SelectItem>
                  <SelectItem value="1080p">1080p</SelectItem>
                  <SelectItem value="720p">720p</SelectItem>
                  <SelectItem value="480p">480p</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}
        />

        <SettingRow
          label="Spoken language"
          description="Prefer a spoken language for this profile when multiple tracks are available."
          control={(id) => (
            <div id={id} className="w-full">
              <Select
                value={profile.language || "none"}
                onValueChange={(value) =>
                  saveProfileField({ language: value === "none" ? "" : value })
                }
              >
                <SelectTrigger className="w-full sm:w-[220px]" disabled={updateMutation.isPending}>
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
          label="Auto-skip intros"
          description="Jump past intros automatically when Silo can detect them."
          control={(id) => (
            <div id={id}>
              <Switch
                checked={profile.auto_skip_intro ?? false}
                disabled={updateMutation.isPending}
                onCheckedChange={(checked) => saveProfileField({ auto_skip_intro: checked })}
              />
            </div>
          )}
        />

        <SettingRow
          label="Auto-skip credits"
          description="Move through end credits automatically when a skip is available."
          control={(id) => (
            <div id={id}>
              <Switch
                checked={profile.auto_skip_credits ?? false}
                disabled={updateMutation.isPending}
                onCheckedChange={(checked) => saveProfileField({ auto_skip_credits: checked })}
              />
            </div>
          )}
        />

        <SettingRow
          label="Auto-skip recaps"
          description="Skip 'previously on…' recaps automatically when Silo can detect them."
          control={(id) => (
            <div id={id}>
              <Switch
                checked={profile.auto_skip_recap ?? false}
                disabled={updateMutation.isPending}
                onCheckedChange={(checked) => saveProfileField({ auto_skip_recap: checked })}
              />
            </div>
          )}
        />

        <SettingRow
          label="Start next at preview"
          description="Begin the next episode when the current one reaches its next-episode preview teaser, rather than waiting for the end credits."
          control={(id) => (
            <div id={id}>
              <Switch
                checked={profile.auto_play_next_preview ?? false}
                disabled={updateMutation.isPending}
                onCheckedChange={(checked) =>
                  saveProfileField({ auto_play_next_preview: checked })
                }
              />
            </div>
          )}
        />

        <AutoPlayNextSetting profileId={profile.id} />

        <NextUpSetting />
      </SettingsGroup>
    </div>
  );
}
