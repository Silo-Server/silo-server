import { useId, useState } from "react";
import { Check, SlidersHorizontal } from "lucide-react";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { BadgeImagesControl } from "@/components/overlays/settings/BadgeImagesControl";
import { OverlayLookControls } from "@/components/overlays/settings/OverlayLookControls";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useShownRatingSources } from "@/hooks/queries/ratingsCapability";
import type { EnabledCardQuickActionMode } from "@/lib/cardQuickActions";
import {
  ACCENT_PALETTE,
  CATEGORY_GROUPS,
  getOverlayDef,
  getPreset,
  isOverlayOffered,
  isOverlaySuppressed,
  OVERLAY_REGISTRY,
  POSITION_OPTIONS,
  WATCH_STATUS_OPTIONS,
  watchStatusOf,
  type CardOverlayPrefs,
  type OverlayId,
  type OverlayImage,
  type OverlayItemConfig,
  type OverlayPosition,
} from "@/lib/overlays";
import { cn } from "@/lib/utils";

import { ChoiceButtons } from "@/components/settings/ChoiceButtons";
import { ResetButton } from "./ResetButton";

type ShortcutChoice = "off" | EnabledCardQuickActionMode;

const SHORTCUT_CHOICES: readonly { value: ShortcutChoice; label: string }[] = [
  { value: "off", label: "Off" },
  { value: "watched", label: "Watched" },
  { value: "favorites", label: "Favorite" },
  { value: "both", label: "Both" },
];

const POSITION_CLASSES: Record<OverlayPosition, string> = {
  "top-left": "top-1 left-1 h-4 w-7",
  "left-edge": "top-1/2 left-0 h-7 w-3 -translate-y-1/2 rounded-l-none",
  "right-edge": "top-1/2 right-0 h-7 w-3 -translate-y-1/2 rounded-r-none",
  "bottom-left": "bottom-1 left-1 h-4 w-7",
  "bottom-right": "bottom-1 right-1 h-4 w-7",
};

interface BadgeAdjustProps {
  overlayId: OverlayId;
  prefs: CardOverlayPrefs;
  shortcutsOn: boolean;
  images: readonly OverlayImage[];
  supportsStyles: boolean;
  onChange: (next: Partial<OverlayItemConfig>) => void;
}

/** Where one badge sits, its color, its icon, and its images, behind one button. */
function BadgeAdjust({
  overlayId,
  prefs,
  shortcutsOn,
  images,
  supportsStyles,
  onChange,
}: BadgeAdjustProps) {
  const def = getOverlayDef(overlayId);
  const iconSwitchId = useId();
  if (!def) return null;
  const config = prefs.items[overlayId];
  const showIcon = !!def.iconCapable && (config.showIcon ?? getPreset(prefs.preset).preferIcon);
  const color = config.accentColor;

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-muted-foreground hover:text-foreground"
          aria-label={`Adjust ${def.label}`}
        >
          <SlidersHorizontal className="h-4 w-4" aria-hidden="true" />
          <span className="hidden sm:inline">Adjust</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="max-h-[80vh] w-80 space-y-4 overflow-y-auto p-4">
        <p className="text-sm font-semibold">{def.label}</p>

        <div className="flex items-start gap-4">
          <div
            role="radiogroup"
            aria-label="Position"
            className="bg-muted/50 border-border/70 relative aspect-[2/3] w-20 shrink-0 rounded-md border"
          >
            {/* The top-right corner is the watch-status indicator's. */}
            <span
              aria-hidden="true"
              className="border-foreground/25 absolute top-1 right-1 h-3.5 w-3.5 rounded-full border border-dashed"
            />
            {POSITION_OPTIONS.map((option) => {
              const active = config.position === option.value;
              return (
                <button
                  key={option.value}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  aria-label={option.label}
                  title={option.label}
                  onClick={() => onChange({ position: option.value })}
                  className={cn(
                    "focus-visible:ring-ring absolute rounded-sm transition-colors focus-visible:ring-2 focus-visible:outline-none",
                    POSITION_CLASSES[option.value],
                    active ? "bg-primary" : "bg-foreground/15 hover:bg-foreground/30",
                  )}
                />
              );
            })}
          </div>
          <div className="space-y-1">
            <p className="text-sm font-medium">Position</p>
            <p className="text-muted-foreground text-[13px] leading-relaxed">
              Pick a corner or a side. Badges in the same place stack. The top-right corner shows
              whether you watched it.
            </p>
            {shortcutsOn && config.position.startsWith("bottom") ? (
              <p className="text-muted-foreground text-[13px] leading-relaxed">
                Shortcut buttons cover the bottom corners while they show.
              </p>
            ) : null}
          </div>
        </div>

        <div className="space-y-2">
          <p className="text-sm font-medium">Color</p>
          <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label="Color">
            <button
              type="button"
              role="radio"
              aria-checked={!color}
              onClick={() => onChange({ accentColor: undefined })}
              className={cn(
                "border-border text-muted-foreground hover:text-foreground focus-visible:ring-ring h-7 rounded-full border px-2.5 text-xs font-medium focus-visible:ring-2 focus-visible:outline-none",
                !color && "border-primary text-foreground",
              )}
            >
              Default
            </button>
            {ACCENT_PALETTE.map((swatch) => (
              <button
                key={swatch.value}
                type="button"
                role="radio"
                aria-checked={color === swatch.value}
                aria-label={swatch.label}
                title={swatch.label}
                onClick={() => onChange({ accentColor: swatch.value })}
                className="border-border/40 focus-visible:ring-ring relative h-7 w-7 rounded-full border focus-visible:ring-2 focus-visible:outline-none"
                style={{ background: swatch.value }}
              >
                {color === swatch.value ? (
                  <Check
                    aria-hidden="true"
                    className="absolute inset-0 m-auto h-3.5 w-3.5"
                    style={{ color: swatch.value === "#ffffff" ? "black" : "white" }}
                  />
                ) : null}
              </button>
            ))}
          </div>
        </div>

        {def.iconCapable ? (
          <div className="flex items-center justify-between gap-3">
            <Label htmlFor={iconSwitchId} className="text-sm font-medium">
              Show icon
            </Label>
            <Switch
              id={iconSwitchId}
              checked={showIcon}
              onCheckedChange={(checked) => onChange({ showIcon: checked })}
            />
          </div>
        ) : null}

        {/* Shown while the badge still names an image, even one since
            deleted, so the reference can be cleared. */}
        {supportsStyles && (images.length > 0 || config.image || config.valueImages) ? (
          <BadgeImagesControl def={def} config={config} images={images} onChange={onChange} />
        ) : null}
      </PopoverContent>
    </Popover>
  );
}

interface BadgeRowProps {
  overlayId: OverlayId;
  prefs: CardOverlayPrefs;
  shortcutsOn: boolean;
  images: readonly OverlayImage[];
  supportsStyles: boolean;
  onUpdate: (next: CardOverlayPrefs) => void;
}

function BadgeRow({
  overlayId,
  prefs,
  shortcutsOn,
  images,
  supportsStyles,
  onUpdate,
}: BadgeRowProps) {
  const switchId = useId();
  const def = getOverlayDef(overlayId);
  if (!def) return null;
  const config = prefs.items[overlayId];
  const suppressed = config.enabled && isOverlaySuppressed(overlayId, prefs);
  const hint = suppressed
    ? "Hidden while the combined Resolution + HDR badge is on."
    : def.availabilityNote;
  const update = (next: Partial<OverlayItemConfig>) =>
    onUpdate({ ...prefs, items: { ...prefs.items, [overlayId]: { ...config, ...next } } });

  return (
    <div className="border-border/50 flex items-center gap-3 border-t py-3 first:border-t-0">
      <div className="min-w-0 flex-1 space-y-0.5">
        <Label htmlFor={switchId} className="text-sm font-medium">
          {def.label}
        </Label>
        <p className="text-muted-foreground text-[13px] leading-relaxed">{def.description}</p>
        {hint ? <p className="text-muted-foreground text-[13px] italic">{hint}</p> : null}
      </div>
      {config.enabled ? (
        <BadgeAdjust
          overlayId={overlayId}
          prefs={prefs}
          shortcutsOn={shortcutsOn}
          images={images}
          supportsStyles={supportsStyles}
          onChange={update}
        />
      ) : null}
      <Switch
        id={switchId}
        checked={config.enabled}
        onCheckedChange={(checked) => update({ enabled: checked })}
      />
    </div>
  );
}

/**
 * Shortcut buttons and badges on media cards. These preferences are
 * profile-wide and stored apart from the web card layout, so they render even
 * when that layout cannot be edited.
 */
export function CardOverlaySettings({ onShortcutsPicked }: { onShortcutsPicked?: () => void }) {
  const shownRatingSources = useShownRatingSources();
  const {
    editablePrefs,
    images,
    supportsStyles,
    setPrefs,
    quickActionPreference,
    setQuickActionMode,
    quickActionsEnabled,
    setQuickActionsEnabled,
    overlaysEnabled,
    setOverlaysEnabled,
    resetPrefs,
    hasBadgeOverride,
    hasQuickActionOverride,
    isResetting,
    isLoading,
    isOverlaySupported,
  } = useOverlayPrefs();
  const [confirmBadgeResetOpen, setConfirmBadgeResetOpen] = useState(false);
  const badgesSwitchId = useId();

  // Removing the profile's stored values is what puts it back on the server
  // defaults for good: a copy of today's server values would freeze this
  // profile at them and ignore every later change the admin makes.
  const resetShortcuts = async () => {
    try {
      await resetPrefs("quickActions");
    } catch {
      toast.error("Couldn't reset shortcuts");
    }
  };

  const resetBadges = async () => {
    try {
      await resetPrefs("badges");
      setConfirmBadgeResetOpen(false);
    } catch {
      toast.error("Couldn't reset badges");
    }
  };

  const pickShortcuts = (next: ShortcutChoice) => {
    if (next === "off") {
      setQuickActionsEnabled(false);
      return;
    }
    if (!quickActionsEnabled) setQuickActionsEnabled(true);
    setQuickActionMode(next);
    onShortcutsPicked?.();
  };

  if (isLoading) return null;
  // The whole document, even while badges are off: the watch-status choice
  // lives in it too, and saving it must keep the badge choices intact.
  const displayPrefs = editablePrefs;

  return (
    <>
      <SettingsGroup
        title="Shortcuts"
        description="Buttons on a poster that mark it watched or add it to your favorites, without opening it."
        actions={
          hasQuickActionOverride ? (
            <ResetButton
              label="Reset shortcuts"
              disabled={isResetting}
              onClick={() => void resetShortcuts()}
            />
          ) : null
        }
      >
        <ChoiceButtons
          label="Shortcut buttons"
          hideLabel
          value={quickActionsEnabled ? quickActionPreference : "off"}
          options={SHORTCUT_CHOICES}
          onChange={pickShortcuts}
        />
      </SettingsGroup>

      {supportsStyles ? (
        <SettingsGroup
          title="Watch status"
          description="The poster's top-right corner: a check once you watch something, and how many episodes a show you started has left."
        >
          <ChoiceButtons
            label="Watch status"
            hideLabel
            value={watchStatusOf(displayPrefs)}
            options={WATCH_STATUS_OPTIONS}
            onChange={(watchStatus) => setPrefs({ ...displayPrefs, watchStatus })}
          />
        </SettingsGroup>
      ) : null}

      <SettingsGroup
        title="Badges"
        description="Small labels on posters, such as 4K, Dolby Vision, or a rating."
        actions={
          hasBadgeOverride ? (
            <ResetButton
              label="Reset badges"
              disabled={isResetting}
              onClick={() => setConfirmBadgeResetOpen(true)}
            />
          ) : null
        }
      >
        <div className="flex items-center justify-between gap-3">
          <Label htmlFor={badgesSwitchId} className="text-sm font-medium">
            Show badges on posters
          </Label>
          <Switch
            id={badgesSwitchId}
            checked={overlaysEnabled}
            onCheckedChange={setOverlaysEnabled}
          />
        </div>

        {overlaysEnabled ? (
          <>
            <div>
              <SettingsSubheading>Style</SettingsSubheading>
              <OverlayLookControls
                prefs={displayPrefs}
                onChange={setPrefs}
                supportsStyles={supportsStyles}
              />
            </div>
            {/* Folded by category, each saying how many are on, so the page
                stays short and the preview stays the main thing to look at. */}
            <Accordion type="multiple" className="border-border/50 border-t">
              {CATEGORY_GROUPS.map(({ category, title }) => {
                const overlays = OVERLAY_REGISTRY.filter(
                  (d) =>
                    d.category === category &&
                    isOverlaySupported(d.id) &&
                    isOverlayOffered(d, shownRatingSources),
                );
                if (overlays.length === 0) return null;
                const onCount = overlays.filter((d) => displayPrefs.items[d.id].enabled).length;
                return (
                  <AccordionItem key={category} value={category} className="border-border/50">
                    <AccordionTrigger className="items-center hover:no-underline">
                      <span className="flex-1">{title}</span>
                      <span className="text-muted-foreground text-xs font-normal tabular-nums">
                        {onCount === 0 ? "None on" : `${onCount} of ${overlays.length} on`}
                      </span>
                    </AccordionTrigger>
                    <AccordionContent className="pb-2">
                      {overlays.map((def) => (
                        <BadgeRow
                          key={def.id}
                          overlayId={def.id}
                          prefs={displayPrefs}
                          shortcutsOn={quickActionsEnabled}
                          images={images}
                          supportsStyles={supportsStyles}
                          onUpdate={setPrefs}
                        />
                      ))}
                    </AccordionContent>
                  </AccordionItem>
                );
              })}
            </Accordion>
          </>
        ) : null}
      </SettingsGroup>

      <ConfirmDialog
        open={confirmBadgeResetOpen}
        onOpenChange={setConfirmBadgeResetOpen}
        title="Reset badges?"
        description="Your badge choices are cleared, and this profile follows your server's badge settings from then on."
        confirmLabel="Reset badges"
        variant="destructive"
        isPending={isResetting}
        onConfirm={() => {
          void resetBadges();
        }}
      />
    </>
  );
}
