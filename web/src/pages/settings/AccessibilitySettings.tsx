import { useId } from "react";
import { toast } from "sonner";
import { captureProfileRequestContext, isProfileRequestContextCurrent } from "@/api/client";
import { PageHeader } from "@/components/page/PageHeader";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  isSettingValueMissing,
  settingsCapabilitiesSupportKey,
  useClearSettingValue,
  useSetSettingValue,
  useSettingsCapabilities,
  useStoredSettingValues,
} from "@/hooks/queries/settingValues";
import { useDateTimeFormat, useDateTimeFormatSettings } from "@/hooks/useDateTimeFormat";
import { useTheme } from "@/hooks/useTheme";
import { PSEUDO_LOCALES_ENABLED, useT, type MessageKey } from "@/i18n";
import { matchAppLanguage, pickerLanguages } from "@/lib/appLocale";
import { formatDate, formatTime } from "@/lib/datetime";
import type { DateFormatPreference, TimeFormatPreference } from "@/lib/datetime";
import { getLanguageName } from "@/lib/languageNames";
import { SETTING_KEYS } from "@/lib/settingsContract";

/** A pattern choice without a `labelKey` is its own label. */
const DATE_FORMAT_CHOICES: ReadonlyArray<{ value: DateFormatPreference; labelKey?: MessageKey }> = [
  { value: "auto", labelKey: "web.settings.accessibility.formatAuto" },
  { value: "DD/MM/YYYY" },
  { value: "MM/DD/YYYY" },
  { value: "YYYY-MM-DD" },
];

const TIME_FORMAT_CHOICES: ReadonlyArray<{ value: TimeFormatPreference; labelKey: MessageKey }> = [
  { value: "auto", labelKey: "web.settings.accessibility.formatAuto" },
  { value: "12h", labelKey: "web.settings.accessibility.time12h" },
  { value: "24h", labelKey: "web.settings.accessibility.time24h" },
];

const APP_LANGUAGE_KEYS = [SETTING_KEYS.UI_APP_LANGUAGE] as const;
const PROFILE_SCOPE = { scope: "profile" } as const;
/** This browser's own value; the server reads the device from X-Silo-Device-Id. */
const THIS_DEVICE_SCOPE = { scope: "profile_device" } as const;
const MATCH_DEVICE = "device";

/**
 * The profile's app language. It edits the profile's own value and then
 * clears this browser's per-device value, so the choice follows the person
 * across devices (a per-device value set elsewhere still wins on its device).
 * A stored tag the web app doesn't ship stays selected under its name, so
 * saving here never clears what another app chose.
 */
function AppLanguageSetting() {
  const t = useT();
  const labelId = useId();
  const { data: stored } = useStoredSettingValues({
    keys: APP_LANGUAGE_KEYS,
    identity: PROFILE_SCOPE,
  });
  const mutation = useSetSettingValue();
  const clearDevice = useClearSettingValue();
  // mutateAsync, not mutate: the clear must still run if the page closes first.
  async function save(next: string) {
    // One save at a time: two in flight could land in either order and
    // leave the earlier choice stored. The select shows the pending one.
    if (mutation.isPending || clearDevice.isPending) return;
    // The clear runs later; skip it if the person switched profile meanwhile.
    const requestContext = captureProfileRequestContext();
    try {
      await mutation.mutateAsync({
        key: SETTING_KEYS.UI_APP_LANGUAGE,
        value: next === MATCH_DEVICE ? null : next,
        identity: PROFILE_SCOPE,
      });
    } catch {
      toast.error(t("web.settings.accessibility.appLanguageSaveFailed"));
      return;
    }
    await clearThisBrowser(requestContext);
  }
  // Deletes this browser's own value so the profile's applies here. Skipped if
  // the profile or session changed since the save; a failure offers a retry,
  // since picking the same language again changes nothing.
  async function clearThisBrowser(requestContext: ReturnType<typeof captureProfileRequestContext>) {
    const current = captureProfileRequestContext();
    if (
      !requestContext ||
      !isProfileRequestContextCurrent(requestContext) ||
      current?.profileId !== requestContext.profileId
    ) {
      return;
    }
    try {
      await clearDevice.mutateAsync({
        key: SETTING_KEYS.UI_APP_LANGUAGE,
        identity: THIS_DEVICE_SCOPE,
      });
    } catch (error) {
      // No per-device value is the usual case.
      if (!isSettingValueMissing(error)) {
        toast.error(t("web.settings.accessibility.appLanguageDeviceClearFailed"), {
          action: {
            label: t("common.tryAgain"),
            onClick: () => void clearThisBrowser(requestContext),
          },
        });
      }
    }
  }
  const savedValue = stored?.[SETTING_KEYS.UI_APP_LANGUAGE];
  const shownValue = mutation.isPending ? mutation.variables.value : savedValue;
  const tag = typeof shownValue === "string" ? shownValue : null;
  const languages = pickerLanguages(PSEUDO_LOCALES_ENABLED);
  const offered = tag !== null && languages.some((language) => language.tag === tag);

  return (
    <div className="space-y-2">
      <p id={labelId} className="text-sm font-medium">
        {t("web.settings.accessibility.appLanguage")}
      </p>
      <Select
        value={tag ?? MATCH_DEVICE}
        disabled={stored === undefined}
        onValueChange={(next) => void save(next)}
      >
        <SelectTrigger aria-labelledby={labelId} className="w-full sm:w-64">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={MATCH_DEVICE}>
            {t("web.settings.accessibility.matchDevice")}
          </SelectItem>
          {tag !== null && !offered && <SelectItem value={tag}>{getLanguageName(tag)}</SelectItem>}
          {languages.map((language) => (
            <SelectItem key={language.tag} value={language.tag} lang={language.tag}>
              {language.endonym}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {tag !== null && matchAppLanguage(tag, PSEUDO_LOCALES_ENABLED) === null && (
        <p className="text-muted-foreground text-[13px]">
          {t("web.settings.accessibility.appLanguageUnavailable")}
        </p>
      )}
    </div>
  );
}

/** Shown once the server knows the setting; an older server has nowhere to save it. */
function AppLanguageGroup() {
  const t = useT();
  const { data: capabilities } = useSettingsCapabilities();
  if (!settingsCapabilitiesSupportKey(capabilities, SETTING_KEYS.UI_APP_LANGUAGE)) return null;
  return (
    <SettingsGroup
      title={t("web.settings.accessibility.languageTitle")}
      description={t("web.settings.accessibility.languageDescription")}
    >
      <AppLanguageSetting />
    </SettingsGroup>
  );
}

export default function AccessibilitySettings() {
  const t = useT();
  const { textScale, setTextScale, textWeight, setTextWeight, highContrast, setHighContrast } =
    useTheme();

  const textSizeLabelId = useId();
  const textWeightLabelId = useId();
  const contrastLabelId = useId();
  const { dateFormat, timeFormat, setDateFormat, setTimeFormat } = useDateTimeFormatSettings();
  useDateTimeFormat();
  const dateFormatLabelId = useId();
  const timeFormatLabelId = useId();
  const previewDate = new Date();

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.accessibility.title")}
        subtitle={t("web.settings.accessibility.subtitle")}
      />

      <AppLanguageGroup />

      <SettingsGroup
        title={t("web.settings.accessibility.readabilityTitle")}
        description={t("web.settings.accessibility.readabilityDescription")}
      >
        <div className="space-y-4">
          <div className="space-y-2">
            <p id={textSizeLabelId} className="text-sm font-medium">
              {t("web.settings.accessibility.textSize")}
            </p>
            <div
              role="radiogroup"
              aria-labelledby={textSizeLabelId}
              className="flex flex-wrap gap-2"
            >
              {[
                { value: "default" as const, label: t("web.settings.accessibility.default") },
                { value: "large" as const, label: t("web.settings.accessibility.textSizeLarge") },
                {
                  value: "x-large" as const,
                  label: t("web.settings.accessibility.textSizeExtraLarge"),
                },
              ].map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={textScale === option.value}
                  variant={textScale === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setTextScale(option.value)}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <p id={textWeightLabelId} className="text-sm font-medium">
              {t("web.settings.accessibility.textWeight")}
            </p>
            <div
              role="radiogroup"
              aria-labelledby={textWeightLabelId}
              className="flex flex-wrap gap-2"
            >
              {[
                { value: "default" as const, label: t("web.settings.accessibility.default") },
                {
                  value: "strong" as const,
                  label: t("web.settings.accessibility.textWeightBolder"),
                },
              ].map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={textWeight === option.value}
                  variant={textWeight === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setTextWeight(option.value)}
                >
                  {option.label}
                </Button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <p id={contrastLabelId} className="text-sm font-medium">
              {t("web.settings.accessibility.contrast")}
            </p>
            <div
              role="radiogroup"
              aria-labelledby={contrastLabelId}
              className="flex flex-wrap gap-2"
            >
              <Button
                role="radio"
                aria-checked={!highContrast}
                variant={!highContrast ? "default" : "outline"}
                size="sm"
                onClick={() => setHighContrast(false)}
              >
                {t("web.settings.accessibility.contrastStandard")}
              </Button>
              <Button
                role="radio"
                aria-checked={highContrast}
                variant={highContrast ? "default" : "outline"}
                size="sm"
                onClick={() => setHighContrast(true)}
              >
                {t("web.settings.accessibility.contrastHigh")}
              </Button>
            </div>
          </div>
        </div>
      </SettingsGroup>

      <SettingsGroup
        title={t("web.settings.accessibility.previewTitle")}
        description={t("web.settings.accessibility.previewDescription")}
      >
        <div className="border-border/50 space-y-2 rounded-lg border p-4">
          <p className="text-lg font-semibold">{t("web.settings.accessibility.previewHeadline")}</p>
          <p className="text-muted-foreground text-sm">
            {t("web.settings.accessibility.previewParagraph")}
          </p>
          <p className="text-muted-foreground text-xs">
            {t("web.settings.accessibility.previewSecondary")}
          </p>
        </div>
      </SettingsGroup>

      <SettingsGroup
        title={t("web.settings.accessibility.dateTimeTitle")}
        description={t("web.settings.accessibility.dateTimeDescription")}
      >
        <div className="space-y-4">
          <div className="space-y-2">
            <p id={dateFormatLabelId} className="text-sm font-medium">
              {t("web.settings.accessibility.dateFormat")}
            </p>
            <div
              role="radiogroup"
              aria-labelledby={dateFormatLabelId}
              className="flex flex-wrap gap-2"
            >
              {DATE_FORMAT_CHOICES.map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={dateFormat === option.value}
                  variant={dateFormat === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setDateFormat(option.value)}
                >
                  {option.labelKey ? t(option.labelKey) : option.value}
                </Button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <p id={timeFormatLabelId} className="text-sm font-medium">
              {t("web.settings.accessibility.timeFormat")}
            </p>
            <div
              role="radiogroup"
              aria-labelledby={timeFormatLabelId}
              className="flex flex-wrap gap-2"
            >
              {TIME_FORMAT_CHOICES.map((option) => (
                <Button
                  key={option.value}
                  role="radio"
                  aria-checked={timeFormat === option.value}
                  variant={timeFormat === option.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setTimeFormat(option.value)}
                >
                  {t(option.labelKey)}
                </Button>
              ))}
            </div>
          </div>

          <p className="text-muted-foreground text-[13px]">
            {t("web.settings.accessibility.dateTimePreview", {
              short: formatDate(previewDate),
              medium: formatDate(previewDate, "medium"),
              time: formatTime(previewDate),
            })}
          </p>
        </div>
      </SettingsGroup>
    </div>
  );
}
