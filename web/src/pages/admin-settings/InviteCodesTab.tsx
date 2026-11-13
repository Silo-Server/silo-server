import { useState } from "react";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { AlertTriangle, Plus } from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";

import { InviteCodeList } from "./InviteCodeList";
import {
  TOUCH_DIALOG_CLOSE_CLASS,
  TOUCH_FIELD_CLASS,
  TOUCH_PRIMARY_ACTION_CLASS,
} from "./touchClasses";
import { SettingsLink } from "./TouchLayoutParts";

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
        className={TOUCH_TARGETS}
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
        <DialogContent className={TOUCH_DIALOG_CLOSE_CLASS}>
          <DialogHeader>
            <DialogTitle>{t("web.admin.settings.inviteCodes.topUp.title")}</DialogTitle>
            <DialogDescription className="break-words">
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
            <Button size="sm" className={TOUCH_PRIMARY_ACTION_CLASS}>
              <Plus className="me-1 h-4 w-4" /> {t("web.admin.settings.inviteCodes.create.button")}
            </Button>
          </DialogTrigger>
          <DialogContent className={TOUCH_DIALOG_CLOSE_CLASS}>
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
              link: (chunks) => <SettingsLink to="/admin/settings/sign-in">{chunks}</SettingsLink>,
            })}
          </p>
        ) : (
          <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" />
            <p className="text-[13px] leading-relaxed">
              {t("web.admin.settings.inviteCodes.signupsOff", {
                b: (chunks) => <span className="font-medium text-amber-500">{chunks}</span>,
                link: (chunks) => (
                  <SettingsLink to="/admin/settings/sign-in">{chunks}</SettingsLink>
                ),
              })}
            </p>
          </div>
        ))}

      {isError && (
        <div role="alert">
          {t("web.admin.settings.inviteCodes.loadFailed", {
            button: (chunks) => (
              <Button className={TOUCH_FIELD_CLASS} onClick={() => void refetch()}>
                {chunks}
              </Button>
            ),
          })}
        </div>
      )}
      <InviteCodeList
        codes={codes}
        onCopy={(code) => void handleCopy(code.code)}
        onToggle={handleToggleCode}
        onTopUp={setTopUpCode}
        onDelete={handleDelete}
      />
      {hasNextPage && (
        <Button
          className={TOUCH_PRIMARY_ACTION_CLASS}
          disabled={isFetchingNextPage}
          onClick={() => void fetchNextPage()}
        >
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
          className={TOUCH_FIELD_CLASS}
          value={code}
          required
          onChange={(e) => setCode(e.target.value.toUpperCase())}
          placeholder={t("web.admin.settings.inviteCodes.form.codePlaceholder")}
        />
      </div>
      <div className="space-y-2">
        <Label>{t("web.admin.settings.inviteCodes.form.label")}</Label>
        <Input
          className={TOUCH_FIELD_CLASS}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder={t("web.admin.settings.inviteCodes.form.labelPlaceholder")}
        />
      </div>
      <div className="space-y-2">
        <Label>{t("web.admin.settings.inviteCodes.form.maxUses")}</Label>
        <Input
          className={TOUCH_FIELD_CLASS}
          type="number"
          min="1"
          value={maxUses}
          onChange={(e) => setMaxUses(e.target.value)}
          required
        />
      </div>
      <Button
        type="submit"
        className={cn("w-full", TOUCH_FIELD_CLASS)}
        disabled={createMutation.isPending}
      >
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
          className={TOUCH_FIELD_CLASS}
          type="number"
          min="1"
          value={additionalUses}
          onChange={(e) => setAdditionalUses(e.target.value)}
          required
        />
      </div>
      <Button
        type="submit"
        className={cn("w-full", TOUCH_FIELD_CLASS)}
        disabled={topUpMutation.isPending}
      >
        {topUpMutation.isPending
          ? t("web.admin.settings.inviteCodes.topUpForm.adding")
          : t("web.admin.settings.inviteCodes.topUpForm.submit")}
      </Button>
    </form>
  );
}
