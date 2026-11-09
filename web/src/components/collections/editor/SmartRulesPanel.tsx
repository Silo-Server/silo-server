import { useId } from "react";

import { RuleBuilder } from "@/components/homeRows/addRow/RuleBuilder";
import { allMyLibraries, personalRulesNote, rulesCaption } from "@/lib/collections/copy";
import { draftRules } from "@/lib/collections/draft";
import type { CollectionDraft, ScopeKind } from "@/lib/collections/scope";

import { LIBRARIES_LINE_ID } from "../fields/librariesLineFocus";
import { OrderBlock } from "../fields/OrderBlock";
import { useT } from "@/i18n";

/**
 * A Smart collection's Contents: the Home rows rule sentence, with the
 * libraries it draws from in it (none: every library, or all my libraries on
 * a personal collection), then Order. A server collection's rules keep their
 * own libraries; where it shows is picked in Where it shows. Personalized rules are offered only on a personal
 * collection; rules the builder can't show stay as locked lines until removed.
 */
export function SmartRulesPanel({
  scopeKind,
  draft,
  onChange,
  libraries,
}: {
  scopeKind: ScopeKind;
  draft: CollectionDraft;
  onChange: (update: (draft: CollectionDraft) => CollectionDraft) => void;
  libraries: Array<{ id: number; name: string }>;
}) {
  const t = useT();
  const id = useId();
  const personal = scopeKind === "personal";
  const rules = draftRules(draft);

  return (
    <section
      aria-labelledby={`${id}-heading`}
      className="surface-panel grid content-start gap-5 rounded-[22px] p-5 sm:p-6"
    >
      <div>
        <h2 id={`${id}-heading`} className="text-[17px] font-semibold">
          {t("web.collections.editor.rulesHeading")}
        </h2>
        <p className="text-muted-foreground mt-1 text-[13.5px]">{rulesCaption()}</p>
      </div>
      <RuleBuilder
        context="collection"
        value={rules}
        libraries={libraries}
        allowPersonalized={personal}
        allLibrariesLabel={personal ? allMyLibraries() : undefined}
        librariesId={LIBRARIES_LINE_ID}
        onChange={(next) =>
          onChange((current) =>
            personal
              ? { ...current, libraryIds: next.library_ids, rules: next }
              : { ...current, rules: next },
          )
        }
      />
      {personal ? (
        <p className="text-muted-foreground -mt-2 text-[12.5px]">{personalRulesNote()}</p>
      ) : null}
      <OrderBlock
        mode="smart"
        rules={rules}
        sortConfig={draft.rawSortConfig}
        allowPersonalized={personal}
        onChange={({ rules: next, sortConfig }) =>
          onChange((current) => ({
            ...current,
            rules: next,
            ...(sortConfig ? { rawSortConfig: sortConfig } : {}),
          }))
        }
      />
    </section>
  );
}
