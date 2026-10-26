import type { SectionOverride, SettingsSectionEntry } from "@/api/types";

/** A server row of the Home Screen settings page. */
export function sectionEntry(
  id: string,
  position: number,
  more: Partial<SettingsSectionEntry> = {},
): SettingsSectionEntry {
  return {
    id,
    section_type: "recently_added",
    title: `Row ${id}`,
    default_title: `Row ${id}`,
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position,
    config: {},
    ...more,
  };
}

/** The server rows with a profile's saved overrides applied, the way the server resolves them. */
export function resolveSectionRows(
  server: SettingsSectionEntry[],
  overrides: SectionOverride[],
): SettingsSectionEntry[] {
  const bySection = new Map(overrides.filter((o) => o.section_id).map((o) => [o.section_id, o]));
  const rows: SettingsSectionEntry[] = [];
  for (const row of server) {
    const o = bySection.get(row.id);
    if (o?.removed) continue;
    rows.push({
      ...row,
      title: o?.title || row.title,
      hidden: o?.hidden ?? false,
      featured: o?.featured ?? row.featured,
      position: o?.position ?? row.position,
      customized: Boolean(o),
    });
  }
  for (const own of overrides.filter((o) => !o.section_id)) {
    rows.push({
      ...sectionEntry(own.id!, own.position ?? 0),
      section_type: own.section_type!,
      title: own.title!,
      default_title: "",
      hidden: Boolean(own.hidden),
      is_custom: true,
      customized: true,
      config: own.config ?? {},
    });
  }
  return rows.sort((a, b) => a.position - b.position);
}
