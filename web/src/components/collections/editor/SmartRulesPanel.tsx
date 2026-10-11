import { useId } from "react";

import { RuleBuilder } from "@/components/homeRows/addRow/RuleBuilder";
import { ALL_MY_LIBRARIES, PERSONAL_RULES_NOTE, RULES_CAPTION } from "@/lib/collections/copy";
import { draftRules } from "@/lib/collections/draft";
import type { CollectionDraft, ScopeKind } from "@/lib/collections/scope";

import { LIBRARIES_LINE_ID } from "../fields/librariesLineFocus";
import { OrderBlock } from "../fields/OrderBlock";

/**
 * A Smart collection's Contents: the Home rows rule sentence, with the
 * libraries in it (server: at least one; personal: none means all my
 * libraries), then Order. Personalized rules are offered only on a personal
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
          Rules
        </h2>
        <p className="text-muted-foreground mt-1 text-[13.5px]">{RULES_CAPTION}</p>
      </div>
      <RuleBuilder
        context="collection"
        value={rules}
        libraries={libraries}
        allowPersonalized={personal}
        librariesRequired={!personal}
        allLibrariesLabel={personal ? ALL_MY_LIBRARIES : undefined}
        librariesId={LIBRARIES_LINE_ID}
        onChange={(next) =>
          onChange((current) => ({ ...current, libraryIds: next.library_ids, rules: next }))
        }
      />
      {personal ? (
        <p className="text-muted-foreground -mt-2 text-[12.5px]">{PERSONAL_RULES_NOTE}</p>
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
