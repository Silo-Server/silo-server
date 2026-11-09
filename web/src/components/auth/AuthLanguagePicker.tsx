import { Languages } from "lucide-react";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { setSignedOutLanguage, useSignedOutLanguage } from "@/hooks/useAppLocale";
import { useAuth } from "@/hooks/useAuth";
import { PSEUDO_LOCALES_ENABLED } from "@/i18n";
import { getLanguageName } from "@/lib/languageNames";
import { matchAppLanguage, pickerLanguages } from "@/lib/appLocale";

const MATCH_DEVICE = "device";

/**
 * Language choice for the signed-out screens, in the shell's top corner. It
 * applies until someone signs in; then their profile's app language, or the
 * browser's, takes over.
 */
export function AuthLanguagePicker() {
  const signedOutLanguage = useSignedOutLanguage();
  // With a profile active, its own setting decides; this picker would do nothing.
  const { user, profile } = useAuth();
  if (user && profile) return null;
  // The cache can hold a profile's regional or unshipped tag; show what applies.
  const value = matchAppLanguage(signedOutLanguage, PSEUDO_LOCALES_ENABLED) ?? MATCH_DEVICE;
  const languages = pickerLanguages(PSEUDO_LOCALES_ENABLED);
  const offered = value === MATCH_DEVICE || languages.some((language) => language.tag === value);

  return (
    <div className="absolute end-4 top-4 z-10">
      <Select
        value={value}
        onValueChange={(next) => setSignedOutLanguage(next === MATCH_DEVICE ? null : next)}
      >
        <SelectTrigger size="sm" aria-label="Language" className="glass panel-border border-0">
          <Languages aria-hidden="true" className="size-4" />
          <SelectValue />
        </SelectTrigger>
        <SelectContent position="popper" align="end">
          <SelectItem value={MATCH_DEVICE}>Match device</SelectItem>
          {!offered && <SelectItem value={value}>{getLanguageName(value)}</SelectItem>}
          {languages.map((language) => (
            <SelectItem key={language.tag} value={language.tag} lang={language.tag}>
              {language.endonym}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}
