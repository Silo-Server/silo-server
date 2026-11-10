import { useState } from "react";
import { copyTextToClipboard } from "@/lib/clipboard";
import { randomUUID } from "@/lib/uuid";
import type { FormEvent } from "react";
import type { InviteCode } from "@/hooks/queries/admin/inviteCodes";
import {
  useAdminInviteCodes,
  useCreateInviteCode,
  useUpdateInviteCode,
  useTopUpInviteCode,
  useDeleteInviteCode,
} from "@/hooks/queries/admin/inviteCodes";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { AlertTriangle, ArrowRight, Copy, Plus, PlusCircle, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { formatDate } from "@/lib/datetime";
import { Link } from "react-router";
import { useT } from "@/i18n";

/** Shown in the Label column for a code without a label. */
const NO_LABEL = "-";

function SignupsSettingLink({ children }: { children: React.ReactNode }) {
  return (
    <Link
      to="/admin/settings/sign-in"
      className="text-foreground inline-flex items-center gap-1 font-medium hover:underline"
    >
      {children}
      <ArrowRight className="h-3 w-3" aria-hidden="true" />
    </Link>
  );
}

export default function InviteCodesTab() {
  const t = useT();
  const {
    data: codes = [],
    isLoading,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
    isError,
    refetch,
  } = useAdminInviteCodes();
  const { data: serverSettings } = useAdminServerSettings();
  const signupsEnabled = serverSettings?.["signup.enabled"] === "true";
  const updateCode = useUpdateInviteCode();
  const deleteCode = useDeleteInviteCode();
  const [createOpen, setCreateOpen] = useState(false);
  const [topUpCode, setTopUpCode] = useState<InviteCode | null>(null);
  const [confirmDeleteCode, setConfirmDeleteCode] = useState<InviteCode | null>(null);

  function handleToggleCode(code: InviteCode) {
    updateCode.mutate({ id: code.id, body: { enabled: !code.enabled } });
  }

  function handleDelete(code: InviteCode) {
    setConfirmDeleteCode(code);
  }

  async function handleCopy(text: string) {
    try {
      await copyTextToClipboard(text);
      toast.success(t("web.admin.settings.inviteCodes.copied"));
    } catch {
      toast.error(t("web.admin.settings.inviteCodes.copyFailed"));
    }
  }

  if (isLoading) return <div>{t("web.admin.settings.inviteCodes.loading")}</div>;

  return (
    <div className="space-y-6">
      <ConfirmDialog
        open={confirmDeleteCode !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteCode(null);
        }}
        title={t("web.admin.settings.inviteCodes.delete.title")}
        description={t("web.admin.settings.inviteCodes.delete.description", {
          code: String(confirmDeleteCode?.code),
        })}
        confirmLabel={t("web.admin.settings.inviteCodes.delete.confirm")}
        variant="destructive"
        onConfirm={() => {
          if (confirmDeleteCode) deleteCode.mutate(confirmDeleteCode.id);
          setConfirmDeleteCode(null);
        }}
      />

      <Dialog
        open={topUpCode !== null}
        onOpenChange={(open) => {
          if (!open) setTopUpCode(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("web.admin.settings.inviteCodes.topUp.title")}</DialogTitle>
            <DialogDescription>
              {t("web.admin.settings.inviteCodes.topUp.description", {
                code: topUpCode?.code ?? "",
                used: topUpCode ? String(topUpCode.use_count) : "",
                max: topUpCode ? String(topUpCode.max_uses) : "",
              })}
            </DialogDescription>
          </DialogHeader>
          {topUpCode && <TopUpInviteCodeForm code={topUpCode} onClose={() => setTopUpCode(null)} />}
        </DialogContent>
      </Dialog>

      <div className="flex justify-end">
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button size="sm">
              <Plus className="me-1 h-4 w-4" /> {t("web.admin.settings.inviteCodes.create.button")}
            </Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{t("web.admin.settings.inviteCodes.create.title")}</DialogTitle>
            </DialogHeader>
            <CreateInviteCodeForm onClose={() => setCreateOpen(false)} />
          </DialogContent>
        </Dialog>
      </div>

      {serverSettings !== undefined &&
        (signupsEnabled ? (
          <p className="text-muted-foreground text-sm">
            {t("web.admin.settings.inviteCodes.signupsOn", {
              link: (chunks) => <SignupsSettingLink>{chunks}</SignupsSettingLink>,
            })}
          </p>
        ) : (
          <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
            <p className="text-[13px] leading-relaxed">
              {t("web.admin.settings.inviteCodes.signupsOff", {
                b: (chunks) => <span className="font-medium text-amber-500">{chunks}</span>,
                link: (chunks) => <SignupsSettingLink>{chunks}</SignupsSettingLink>,
              })}
            </p>
          </div>
        ))}

      {isError && (
        <div role="alert">
          {t("web.admin.settings.inviteCodes.loadFailed", {
            button: (chunks) => <Button onClick={() => void refetch()}>{chunks}</Button>,
          })}
        </div>
      )}
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
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-6 w-6"
                    onClick={() => void handleCopy(code.code)}
                  >
                    <Copy className="h-3 w-3" />
                  </Button>
                </div>
              </TableCell>
              <TableCell>
                {code.label || <span className="text-muted-foreground">{NO_LABEL}</span>}
              </TableCell>
              <TableCell>
                <span className={code.use_count >= code.max_uses ? "text-destructive" : ""}>
                  {t("web.admin.settings.inviteCodes.usageCount", {
                    used: String(code.use_count),
                    max: String(code.max_uses),
                  })}
                </span>
              </TableCell>
              <TableCell>
                <div className="flex items-center gap-2">
                  <Switch checked={code.enabled} onCheckedChange={() => handleToggleCode(code)} />
                  <Badge variant={code.enabled ? "outline" : "secondary"}>
                    {code.enabled
                      ? t("web.admin.settings.inviteCodes.status.active")
                      : t("web.admin.settings.inviteCodes.status.disabled")}
                  </Badge>
                </div>
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
                    onClick={() => setTopUpCode(code)}
                    aria-label={t("web.admin.settings.inviteCodes.topUpLabel", { code: code.code })}
                  >
                    <PlusCircle className="h-3 w-3" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7"
                    onClick={() => handleDelete(code)}
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
      {hasNextPage && (
        <Button disabled={isFetchingNextPage} onClick={() => void fetchNextPage()}>
          {t("web.admin.settings.inviteCodes.loadMore")}
        </Button>
      )}
    </div>
  );
}

function CreateInviteCodeForm({ onClose }: { onClose: () => void }) {
  const t = useT();
  const [code, setCode] = useState(() => randomUUID().replace(/-/g, "").slice(0, 16).toUpperCase());
  const [label, setLabel] = useState("");
  const [maxUses, setMaxUses] = useState("10");
  const createMutation = useCreateInviteCode();

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const max = parseInt(maxUses, 10);
    if (isNaN(max) || max <= 0) {
      toast.error(t("web.admin.settings.inviteCodes.form.maxUsesInvalid"));
      return;
    }
    createMutation.mutate({ code, label, max_uses: max }, { onSuccess: onClose });
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label>{t("web.admin.settings.inviteCodes.form.code")}</Label>
        <Input
          value={code}
          required
          onChange={(e) => setCode(e.target.value.toUpperCase())}
          placeholder={t("web.admin.settings.inviteCodes.form.codePlaceholder")}
        />
      </div>
      <div className="space-y-2">
        <Label>{t("web.admin.settings.inviteCodes.form.label")}</Label>
        <Input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder={t("web.admin.settings.inviteCodes.form.labelPlaceholder")}
        />
      </div>
      <div className="space-y-2">
        <Label>{t("web.admin.settings.inviteCodes.form.maxUses")}</Label>
        <Input
          type="number"
          min="1"
          value={maxUses}
          onChange={(e) => setMaxUses(e.target.value)}
          required
        />
      </div>
      <Button type="submit" className="w-full" disabled={createMutation.isPending}>
        {createMutation.isPending
          ? t("web.admin.settings.inviteCodes.form.creating")
          : t("web.admin.settings.inviteCodes.form.create")}
      </Button>
    </form>
  );
}

function TopUpInviteCodeForm({ code, onClose }: { code: InviteCode; onClose: () => void }) {
  const t = useT();
  const [additionalUses, setAdditionalUses] = useState("1");
  const topUpMutation = useTopUpInviteCode();

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const amount = parseInt(additionalUses, 10);
    if (isNaN(amount) || amount <= 0) {
      toast.error(t("web.admin.settings.inviteCodes.topUpForm.invalid"));
      return;
    }

    topUpMutation.mutate(
      { id: code.id, body: { additional_uses: amount } },
      { onSuccess: onClose },
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      <div className="space-y-2">
        <Label>{t("web.admin.settings.inviteCodes.topUpForm.additionalUses")}</Label>
        <Input
          type="number"
          min="1"
          value={additionalUses}
          onChange={(e) => setAdditionalUses(e.target.value)}
          required
        />
      </div>
      <Button type="submit" className="w-full" disabled={topUpMutation.isPending}>
        {topUpMutation.isPending
          ? t("web.admin.settings.inviteCodes.topUpForm.adding")
          : t("web.admin.settings.inviteCodes.topUpForm.submit")}
      </Button>
    </form>
  );
}
