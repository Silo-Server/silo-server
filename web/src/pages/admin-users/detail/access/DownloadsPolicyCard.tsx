import { Link } from "react-router";

import type { AdminUser, UpdateUserRequest } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";

import { userDetailTabSearch } from "../userDetailTabs";
import { EditableCard, type AccessCardProps } from "./EditableCard";
import { ChoicePolicyEdit, PolicyValueRow } from "./PolicyRow";
import {
  formatAllowed,
  inheritedValueText,
  rowChanged,
  rowDraft,
  rowOverride,
  rowSource,
  type RowDraft,
  ALLOWED_OPTIONS,
} from "./policySources";
import { useAccountCardDraft } from "./useAccountCardDraft";

interface DownloadsDraft {
  download: RowDraft<boolean>;
  serverPrepared: RowDraft<boolean>;
}

const LABELS = {
  download: "Offline downloads",
  serverPrepared: "Server-prepared downloads",
} as const;

const SERVER_PREPARED_DESCRIPTION = "The server converts a smaller copy to download";

function toDraft(user: AdminUser): DownloadsDraft {
  return {
    download: rowDraft(user.download_allowed),
    serverPrepared: rowDraft(user.download_transcode_allowed),
  };
}

function toBody(d: DownloadsDraft, base: AdminUser): UpdateUserRequest {
  const body: UpdateUserRequest = {};
  const download = rowOverride(d.download);
  if (download !== undefined && rowChanged(d.download, base.download_allowed)) {
    body.download_allowed = download;
  }
  const prepared = rowOverride(d.serverPrepared);
  if (prepared !== undefined && rowChanged(d.serverPrepared, base.download_transcode_allowed)) {
    body.download_transcode_allowed = prepared;
  }
  return body;
}

function changedRows(d: DownloadsDraft, base: DownloadsDraft): string[] {
  const rows: string[] = [];
  if (rowChanged(d.download, rowOverride(base.download) ?? null)) rows.push(LABELS.download);
  if (rowChanged(d.serverPrepared, rowOverride(base.serverPrepared) ?? null)) {
    rows.push(LABELS.serverPrepared);
  }
  return rows;
}

export function DownloadsPolicyCard({
  user,
  editor,
  manageable,
  available,
  libraries,
  ctx,
  hints,
}: AccessCardProps) {
  const capabilities = useAdminUserCapabilities();
  const draft = useAccountCardDraft({ id: "downloads", editor, toDraft, toBody, changedRows });
  const d = draft.draft;
  const base = draft.base ?? user;

  return (
    <EditableCard
      id="downloads"
      manageable={manageable}
      available={available}
      canEdit={editor !== undefined}
      state={draft}
      actions={
        capabilities.data?.account_downloads === true ? (
          <Button asChild variant="ghost" size="xs">
            <Link to={userDetailTabSearch("downloads")}>On devices →</Link>
          </Button>
        ) : null
      }
    >
      {draft.editing && d ? (
        <>
          <ChoicePolicyEdit
            label={LABELS.download}
            row={d.download}
            saved={base.download_allowed}
            inherited={hints.download_allowed}
            options={ALLOWED_OPTIONS}
            onChange={(download) => draft.setDraft((prev) => ({ ...prev, download }))}
          />
          <ChoicePolicyEdit
            label={LABELS.serverPrepared}
            description={SERVER_PREPARED_DESCRIPTION}
            row={d.serverPrepared}
            saved={base.download_transcode_allowed}
            inherited={hints.download_transcode_allowed}
            options={ALLOWED_OPTIONS}
            onChange={(serverPrepared) => draft.setDraft((prev) => ({ ...prev, serverPrepared }))}
          />
        </>
      ) : (
        <>
          <PolicyValueRow
            label={LABELS.download}
            value={formatAllowed(user.effective_policy.download_allowed)}
            source={rowSource(user, "downloads", ctx)}
            base={inheritedValueText("downloads", hints, ctx, libraries)}
          />
          <PolicyValueRow
            label={LABELS.serverPrepared}
            description={SERVER_PREPARED_DESCRIPTION}
            value={formatAllowed(user.effective_policy.download_transcode_allowed)}
            source={rowSource(user, "serverPrepared", ctx)}
            base={inheritedValueText("serverPrepared", hints, ctx, libraries)}
          />
        </>
      )}
    </EditableCard>
  );
}
