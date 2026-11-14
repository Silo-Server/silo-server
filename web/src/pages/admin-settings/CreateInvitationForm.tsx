import { useId, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { AlertTriangle } from "lucide-react";

import {
  captureInvitationAuthority,
  type InvitationDelivery,
  type InvitationDeliveryChoice,
} from "@/api/v2/invitations";
import { ChoiceOption } from "@/components/ChoiceOption";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
import { effectiveAccessGroupID } from "@/components/UserPolicyFields";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useCreateInvitation } from "@/hooks/queries/admin/invitations";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useViewerIsOwner } from "@/hooks/queries/admin/users";
import { useAuth } from "@/hooks/useAuth";
import { useT } from "@/i18n";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { cn } from "@/lib/utils";

import { ClaimLinkBox } from "./InvitationDisplay";
import { deliveryMessage, useMounted } from "./invitationRules";
import {
  FULL_SCREEN_BOTTOM_CLASS,
  PINNED_FOOTER_CLASS,
  TOUCH_FIELD_CLASS,
  TOUCH_SELECT_CLASS,
  TOUCH_SELECT_ITEM_CLASS,
  TOUCH_SWITCH_CLASS,
} from "./touchClasses";
import { SettingsLink } from "./TouchLayoutParts";

/** Example address in the email box. */
const EMAIL_PLACEHOLDER = "them@example.com";

export function CreateInvitationForm({
  onClose,
  onCopy,
  defaultProfile,
  emailDelivery,
  onBusy,
  onReload,
}: {
  defaultProfile: boolean;
  emailDelivery: boolean;
  onBusy: (value: boolean) => void;
  onReload: () => Promise<void>;
  onClose: () => void;
  onCopy: (text: string) => Promise<void>;
}) {
  const t = useT();
  const create = useCreateInvitation();
  const { data: accessGroups = [] } = useAccessGroups();
  const { data: libraries = [] } = useAdminLibraries();
  const [delivery, setDelivery] = useState<InvitationDeliveryChoice>(
    emailDelivery ? "email" : "link",
  );
  const deliveryGroup = useId();
  const [email, setEmail] = useState("");
  const [emailInvalid, setEmailInvalid] = useState(false);
  const [role, setRole] = useState<"user" | "admin">("user");
  // Only the server Owner may invite an admin; the server refuses anyone else.
  const viewerId = useAuth().user?.id;
  const viewerIsOwner = useViewerIsOwner(viewerId);
  const [accessGroupID, setAccessGroupID] = useState<number | null>(null);
  const [libraryIDs, setLibraryIDs] = useState<number[] | null>(null);
  const [note, setNote] = useState("");
  const [createProfile, setCreateProfile] = useState(defaultProfile);
  const [showTour, setShowTour] = useState(true);
  // After creation we keep the dialog open to show the claim link — the
  // token is only readable in this response, so this is the one chance to
  // copy it. The delivery outcome describes what the sender confirmed.
  const [result, setResult] = useState<InvitationDelivery | null>(null);
  const [error, setError] = useState("");
  const [needsReload, setNeedsReload] = useState(false);
  const busy = useRef(false);
  const mounted = useMounted();
  const [profileContext] = useState(captureInvitationAuthority);
  const defaultGroup = useMemo(() => accessGroups.find((g) => g.is_default), [accessGroups]);
  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (busy.current || needsReload || (!defaultProfile && createProfile)) return;
    if (delivery === "email" && !isValidEmail(email)) {
      setEmailInvalid(true);
      return;
    }
    busy.current = true;
    onBusy(true);
    setError("");
    const group = effectiveAccessGroupID(role, accessGroupID);
    try {
      const delivered = await create.mutateAsync({
        profileContext,
        body: {
          delivery,
          email: delivery === "email" ? email : undefined,
          role,
          access_group_id: group == null ? undefined : String(group),
          library_ids: libraryIDs == null ? undefined : libraryIDs.map(String),
          create_profile: createProfile,
          show_tour: showTour,
          note: note.trim() || undefined,
        },
      });
      if (mounted.current) setResult(delivered);
    } catch (err) {
      if (mounted.current) {
        setError(
          err instanceof Error ? err.message : t("web.admin.settings.invitations.create.failed"),
        );
        setNeedsReload(true);
      }
    } finally {
      create.reset();
      busy.current = false;
      onBusy(false);
    }
  }

  if (result) {
    return (
      <div className={cn("min-w-0 space-y-4", FULL_SCREEN_BOTTOM_CLASS)}>
        <p className="text-sm">{deliveryMessage(result, t)}</p>
        <ClaimLinkBox
          claimUrl={result.claim_url}
          finePrint={
            result.delivery_status === "not_requested"
              ? t("web.admin.settings.invitations.create.finePrintLink")
              : t("web.admin.settings.invitations.create.finePrintEmail")
          }
          onCopy={onCopy}
          onDone={onClose}
        />
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {error && (
        <div role="alert">
          <p>{error}</p>
          <p>{t("web.admin.settings.invitations.create.reloadBeforeRetry")}</p>
          <Button
            type="button"
            className={TOUCH_FIELD_CLASS}
            onClick={async () => {
              try {
                await onReload();
              } catch {
                if (mounted.current)
                  setError(t("web.admin.settings.invitations.create.reloadFailed"));
                return;
              }
              if (mounted.current) {
                setNeedsReload(false);
                setError("");
              }
            }}
          >
            {t("web.admin.settings.invitations.reloadHistory")}
          </Button>
        </div>
      )}
      {!defaultProfile && (
        <p role="status">{t("web.admin.settings.invitations.create.noDefaultProfile")}</p>
      )}
      <div className="space-y-2">
        <p id={`${deliveryGroup}-label`} className="text-sm leading-none font-medium">
          {t("web.admin.settings.invitations.create.deliveryQuestion")}
        </p>
        <div
          role="radiogroup"
          aria-labelledby={`${deliveryGroup}-label`}
          className="grid gap-2 sm:grid-cols-2"
        >
          <ChoiceOption
            name={deliveryGroup}
            value="link"
            selected={delivery === "link"}
            disabled={create.isPending}
            title={t("web.admin.settings.invitations.create.linkOption")}
            onSelect={setDelivery}
          >
            {t("web.admin.settings.invitations.create.linkOptionBody")}
          </ChoiceOption>
          <ChoiceOption
            name={deliveryGroup}
            value="email"
            selected={delivery === "email"}
            disabled={!emailDelivery || create.isPending}
            title={t("web.admin.settings.invitations.create.emailOption")}
            onSelect={setDelivery}
          >
            {emailDelivery
              ? t("web.admin.settings.invitations.create.emailOptionBody")
              : t("web.admin.settings.invitations.create.emailOptionUnavailable")}
          </ChoiceOption>
        </div>
        {!emailDelivery && (
          <div className="flex items-start gap-3 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" aria-hidden="true" />
            <p className="text-[13px] leading-relaxed">
              {t("web.admin.settings.invitations.create.emailSetup", {
                link: (chunks) => <SettingsLink to="/admin/settings/email">{chunks}</SettingsLink>,
              })}
            </p>
          </div>
        )}
      </div>

      {delivery === "email" && (
        <div className="space-y-2">
          <Label htmlFor="invitation-email">
            {t("web.admin.settings.invitations.create.emailLabel")}
          </Label>
          <Input
            id="invitation-email"
            type="email"
            className={TOUCH_FIELD_CLASS}
            value={email}
            onChange={(e) => {
              setEmail(e.target.value);
              if (emailInvalid) setEmailInvalid(false);
            }}
            placeholder={EMAIL_PLACEHOLDER}
            aria-invalid={emailInvalid || undefined}
            aria-describedby={emailInvalid ? "invitation-email-error" : undefined}
            autoFocus
            required
          />
          {emailInvalid ? (
            <p id="invitation-email-error" className="text-destructive text-xs">
              {invalidEmailMessage()}
            </p>
          ) : null}
          <p className="text-muted-foreground text-xs">
            {t("web.admin.settings.invitations.create.emailHint")}
          </p>
        </div>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-2">
          <Label>{t("web.admin.settings.invitations.create.accessGroup")}</Label>
          <Select
            value={role === "admin" || accessGroupID === null ? "default" : String(accessGroupID)}
            onValueChange={(v) => setAccessGroupID(v === "default" ? null : Number(v))}
            disabled={role === "admin"}
          >
            <SelectTrigger className={TOUCH_SELECT_CLASS}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="default" className={TOUCH_SELECT_ITEM_CLASS}>
                {defaultGroup
                  ? t("web.admin.settings.invitations.create.defaultGroup", {
                      name: defaultGroup.name,
                    })
                  : t("web.admin.settings.invitations.create.serverDefault")}
              </SelectItem>
              {accessGroups
                .filter((g) => !g.is_default)
                .map((g) => (
                  <SelectItem key={g.id} value={String(g.id)} className={TOUCH_SELECT_ITEM_CLASS}>
                    {g.name}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label>{t("web.admin.settings.invitations.create.role")}</Label>
          <Select value={role} onValueChange={(value) => setRole(value as "user" | "admin")}>
            <SelectTrigger className={TOUCH_SELECT_CLASS}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user" className={TOUCH_SELECT_ITEM_CLASS}>
                {t("web.admin.settings.invitations.create.roleUser")}
              </SelectItem>
              <SelectItem
                value="admin"
                disabled={!viewerIsOwner}
                className={TOUCH_SELECT_ITEM_CLASS}
              >
                {t("web.admin.settings.invitations.create.roleAdmin")}
              </SelectItem>
            </SelectContent>
          </Select>
          {!viewerIsOwner && (
            <p className="text-muted-foreground text-xs">
              {t("web.admin.settings.invitations.create.ownerOnly")}
            </p>
          )}
        </div>
      </div>

      <LibraryAccessSelector
        libraries={libraries}
        value={libraryIDs}
        onChange={setLibraryIDs}
        allLabel={t("web.admin.settings.invitations.create.inheritGroup")}
        emptyHint={t("web.admin.settings.invitations.create.libraryHint")}
      />

      <div className="space-y-2">
        <Label htmlFor="invitation-note">
          {delivery === "link"
            ? t("web.admin.settings.invitations.create.noteLabel")
            : t("web.admin.settings.invitations.create.personalNoteLabel")}
        </Label>
        <textarea
          id="invitation-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder={t("web.admin.settings.invitations.create.notePlaceholder")}
          rows={2}
          // 16px below sm so focusing it does not zoom the page on iOS.
          className="border-border bg-background text-foreground focus:border-ring focus:ring-ring/50 w-full resize-y rounded-md border px-3 py-2.5 text-sm shadow-xs transition-[color,box-shadow] outline-none focus:ring-[3px] max-sm:text-base"
        />
        <p className="text-muted-foreground text-xs">
          {delivery === "link"
            ? t("web.admin.settings.invitations.create.noteHintLink")
            : t("web.admin.settings.invitations.create.noteHintEmail")}
        </p>
      </div>

      <div className="space-y-3">
        <div className="flex items-center justify-between max-lg:gap-4">
          <div>
            <Label htmlFor="invitation-create-profile">
              {t("web.admin.settings.invitations.create.profileLabel")}
            </Label>
            <p className="text-muted-foreground text-xs">
              {delivery === "link"
                ? t("web.admin.settings.invitations.create.profileHintLink")
                : t("web.admin.settings.invitations.create.profileHintEmail")}
            </p>
          </div>
          <Switch
            id="invitation-create-profile"
            className={TOUCH_SWITCH_CLASS}
            checked={createProfile}
            onCheckedChange={setCreateProfile}
            disabled={!defaultProfile && !createProfile}
          />
        </div>
        <div className="flex items-center justify-between max-lg:gap-4">
          <div>
            <Label htmlFor="invitation-show-tour">
              {t("web.admin.settings.invitations.create.tourLabel")}
            </Label>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.settings.invitations.create.tourHint")}
            </p>
          </div>
          <Switch
            id="invitation-show-tour"
            className={TOUCH_SWITCH_CLASS}
            checked={showTour}
            onCheckedChange={setShowTour}
          />
        </div>
      </div>

      <div
        className={cn(
          "flex items-center justify-between pt-2 max-sm:flex-col max-sm:items-stretch max-sm:gap-3",
          PINNED_FOOTER_CLASS,
        )}
      >
        <p className="text-muted-foreground text-xs">
          {t("web.admin.settings.invitations.create.expiry")}
        </p>
        <div className="flex gap-2">
          <Button
            type="button"
            variant="ghost"
            className={cn(TOUCH_FIELD_CLASS, "max-sm:flex-1")}
            disabled={create.isPending}
            onClick={onClose}
          >
            {t("common.cancel")}
          </Button>
          <Button
            type="submit"
            className={cn(TOUCH_FIELD_CLASS, "max-sm:flex-1")}
            disabled={create.isPending || needsReload || (!defaultProfile && createProfile)}
          >
            {delivery === "link"
              ? create.isPending
                ? t("web.admin.settings.invitations.create.creating")
                : t("web.admin.settings.invitations.create.submitLink")
              : create.isPending
                ? t("web.admin.settings.invitations.create.sending")
                : t("web.admin.settings.invitations.create.submitEmail")}
          </Button>
        </div>
      </div>
    </form>
  );
}
