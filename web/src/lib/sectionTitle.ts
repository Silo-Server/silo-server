import { useCallback } from "react";

import type { SectionTitleKey } from "@/api/types";
import { useServerText } from "@/i18n";

/**
 * The title key a v2 section, layout entry or profile section setting carries.
 * An empty key counts as none. Until the generated v2 schema declares the
 * fields, this reads them off the response object.
 */
export function sectionTitleKey(section: object): SectionTitleKey {
  const { title_key, title_args } = section as SectionTitleKey;
  if (typeof title_key !== "string" || title_key === "") return {};
  return title_args ? { title_key, title_args } : { title_key };
}

/**
 * A section's title for display. The server's `title` stays the stored value
 * (edits and pins keep using it); a translated interface shows the catalog's
 * text for its `title_key`, filled from `title_args`, and falls back to `title`
 * when the key or an argument is missing. English shows `title` as sent.
 */
export function useSectionTitle(): (section: { title: string } & SectionTitleKey) => string {
  const serverText = useServerText();
  return useCallback(
    (section) => serverText(section.title_key, section.title_args, section.title),
    [serverText],
  );
}
