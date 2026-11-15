import { Copy, PlusCircle, Trash2 } from "lucide-react";

import {
  CARD_ACTION_CLASS,
  CardActions,
  CardField,
  CardFields,
  CardListMessage,
  DiagnosticsCard,
  DiagnosticsCardList,
} from "@/components/admin/libraries/DiagnosticsListParts";
import { useLibrariesTableLayout } from "@/components/admin/libraries/diagnosticsListState";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { InviteCode } from "@/hooks/queries/admin/inviteCodes";
import { useT } from "@/i18n";
import { formatDate } from "@/lib/datetime";
import { cn } from "@/lib/utils";

import { TOUCH_SWITCH_CLASS } from "./touchClasses";

/** Shown in the Label column for a code without a label. */
const NO_LABEL = "-";

export interface InviteCodeListProps {
  codes: InviteCode[];
  onCopy: (code: InviteCode) => void;
  onToggle: (code: InviteCode) => void;
  onTopUp: (code: InviteCode) => void;
  onDelete: (code: InviteCode) => void;
}

/** The invite codes: a table from `lg` up, cards below it. */
export function InviteCodeList(props: InviteCodeListProps) {
  const t = useT();
  const showTable = useLibrariesTableLayout();
  const { codes } = props;

  if (!showTable) {
    if (codes.length === 0) {
      return <CardListMessage>{t("web.admin.settings.inviteCodes.empty")}</CardListMessage>;
    }
    return (
      <DiagnosticsCardList>
        {codes.map((code) => (
          <InviteCodeCard key={code.id} code={code} {...props} />
        ))}
      </DiagnosticsCardList>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t("web.admin.settings.inviteCodes.table.code")}</TableHead>
          <TableHead>{t("web.admin.settings.inviteCodes.table.label")}</TableHead>
          <TableHead>{t("web.admin.settings.inviteCodes.table.usage")}</TableHead>
          <TableHead>{t("web.admin.settings.inviteCodes.table.status")}</TableHead>
          <TableHead>{t("web.admin.settings.inviteCodes.table.created")}</TableHead>
          <TableHead className="w-32">
            {t("web.admin.settings.inviteCodes.table.actions")}
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {codes.length === 0 && (
          <TableRow>
            <TableCell colSpan={6} className="text-muted-foreground text-center">
              {t("web.admin.settings.inviteCodes.empty")}
            </TableCell>
          </TableRow>
        )}
        {codes.map((code) => (
          <TableRow key={code.id}>
            <TableCell>
              <div className="flex items-center gap-1.5">
                <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">
                  {code.code}
                </code>
                <CopyCodeButton code={code} onCopy={props.onCopy} className="h-6 w-6" />
              </div>
            </TableCell>
            <TableCell>
              <CodeLabel code={code} />
            </TableCell>
            <TableCell>
              <CodeUsage code={code} />
            </TableCell>
            <TableCell>
              <CodeStatus code={code} onToggle={props.onToggle} />
            </TableCell>
            <TableCell className="text-muted-foreground text-xs">
              {formatDate(code.created_at)}
            </TableCell>
            <TableCell>
              <div className="flex items-center gap-1">
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  onClick={() => props.onTopUp(code)}
                  aria-label={t("web.admin.settings.inviteCodes.topUpLabel", { code: code.code })}
                >
                  <PlusCircle className="h-3 w-3" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7"
                  onClick={() => props.onDelete(code)}
                  aria-label={t("web.admin.settings.inviteCodes.deleteLabel", {
                    code: code.code,
                  })}
                >
                  <Trash2 className="h-3 w-3" />
                </Button>
              </div>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function InviteCodeCard({
  code,
  onCopy,
  onToggle,
  onTopUp,
  onDelete,
}: Omit<InviteCodeListProps, "codes"> & { code: InviteCode }) {
  const t = useT();
  return (
    <DiagnosticsCard>
      <div className="flex items-start gap-2">
        <code
          className="bg-muted mt-1.5 min-w-0 flex-1 rounded px-2 py-1 font-mono text-sm break-all"
          dir="ltr"
        >
          {code.code}
        </code>
        <CopyCodeButton code={code} onCopy={onCopy} className="-me-2 size-11" />
      </div>

      <CardFields>
        <CardField label={t("web.admin.settings.inviteCodes.table.label")}>
          <CodeLabel code={code} />
        </CardField>
        <CardField label={t("web.admin.settings.inviteCodes.table.usage")}>
          <CodeUsage code={code} />
        </CardField>
        <CardField label={t("web.admin.settings.inviteCodes.table.status")}>
          <CodeStatus code={code} onToggle={onToggle} touch />
        </CardField>
        <CardField label={t("web.admin.settings.inviteCodes.table.created")}>
          {formatDate(code.created_at)}
        </CardField>
      </CardFields>

      <CardActions>
        <Button variant="outline" className={CARD_ACTION_CLASS} onClick={() => onTopUp(code)}>
          <PlusCircle aria-hidden="true" />
          {t("web.admin.settings.inviteCodes.topUpForm.submit")}
        </Button>
        <Button
          variant="outline"
          className={cn(CARD_ACTION_CLASS, "text-destructive hover:text-destructive")}
          onClick={() => onDelete(code)}
          aria-label={t("web.admin.settings.inviteCodes.deleteLabel", { code: code.code })}
        >
          <Trash2 aria-hidden="true" />
          {t("web.admin.settings.inviteCodes.delete.confirm")}
        </Button>
      </CardActions>
    </DiagnosticsCard>
  );
}

function CopyCodeButton({
  code,
  onCopy,
  className,
}: {
  code: InviteCode;
  onCopy: (code: InviteCode) => void;
  className: string;
}) {
  const t = useT();
  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn("shrink-0", className)}
      onClick={() => onCopy(code)}
      aria-label={t("web.settings.watchProviders.auth.copyCode")}
    >
      <Copy className="h-3 w-3" />
    </Button>
  );
}

function CodeLabel({ code }: { code: InviteCode }) {
  return code.label ? (
    <span className="break-words">{code.label}</span>
  ) : (
    <span className="text-muted-foreground">{NO_LABEL}</span>
  );
}

function CodeUsage({ code }: { code: InviteCode }) {
  const t = useT();
  return (
    <span className={code.use_count >= code.max_uses ? "text-destructive" : ""}>
      {t("web.admin.settings.inviteCodes.usageCount", {
        used: code.use_count,
        max: code.max_uses,
      })}
    </span>
  );
}

function CodeStatus({
  code,
  onToggle,
  touch = false,
}: {
  code: InviteCode;
  onToggle: (code: InviteCode) => void;
  touch?: boolean;
}) {
  const t = useT();
  return (
    <div className="flex items-center gap-2">
      <Switch
        className={touch ? TOUCH_SWITCH_CLASS : undefined}
        checked={code.enabled}
        onCheckedChange={() => onToggle(code)}
        aria-label={t("web.admin.settings.inviteCodes.table.status")}
      />
      <Badge variant={code.enabled ? "outline" : "secondary"}>
        {code.enabled
          ? t("web.admin.settings.inviteCodes.status.active")
          : t("web.admin.settings.inviteCodes.status.disabled")}
      </Badge>
    </div>
  );
}
