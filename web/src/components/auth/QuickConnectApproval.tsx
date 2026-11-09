import { useState } from "react";
import { captureProfileRequestContext, captureProfileRequestContextAs } from "@/api/client";
import { v2, V2ProblemError } from "@/api/v2/request";
import { useProfiles, verifyProfilePIN } from "@/hooks/queries/profiles";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioCardItem, RadioGroup } from "@/components/ui/radio-group";
import { tooManyCodesMessage } from "@/lib/deviceCode";
import { useT } from "@/i18n";

interface QuickConnectApprovalProps {
  /** The six-digit code being approved. */
  code: string;
  /** The requesting device's name; empty when it sent none. */
  deviceName: string;
  /** The profile the browser is using now, picked first. */
  activeProfileId: string | null;
  /** Set while the parent is deciding (declining), to hold the buttons. */
  busy: boolean;
  onApproved: (profileName: string) => void;
  /** The request moved on (decided, withdrawn or expired); reload it. */
  onMoved: () => void;
  onDeny: () => void;
}

/**
 * Approves a Jellyfin Quick Connect request. A Jellyfin app's session belongs
 * to one profile, so the person picks it here; a profile with a PIN needs its
 * PIN, unless it is the profile this browser already unlocked.
 */
export function QuickConnectApproval({
  code,
  deviceName,
  activeProfileId,
  busy,
  onApproved,
  onMoved,
  onDeny,
}: QuickConnectApprovalProps) {
  const { data: profiles, isLoading, isError, refetch } = useProfiles();
  const [chosenId, setChosenId] = useState<string | null>(null);
  const [pin, setPin] = useState("");
  const [acting, setActing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const t = useT();

  const selectedId =
    chosenId ??
    (profiles.some((p) => p.id === activeProfileId) ? activeProfileId : profiles[0]?.id) ??
    null;
  const selected = profiles.find((p) => p.id === selectedId) ?? null;
  // The active profile's PIN token proves the PIN for that profile only.
  const active = captureProfileRequestContext();
  const unlockedToken =
    selected && active?.profileId === selected.id ? (active.profileToken ?? null) : null;
  const needsPin = Boolean(selected?.has_pin) && !unlockedToken;

  async function approve() {
    if (!selected) return;
    setActing(true);
    setError(null);
    try {
      let profileToken = unlockedToken;
      if (needsPin) {
        const verified = await verifyProfilePIN(selected.id, pin).catch((err: unknown) => {
          // A PIN lockout is the profile's own limit, not the code budget.
          if (err instanceof V2ProblemError && err.status === 429) {
            const minutes = Math.max(1, Math.ceil((err.retryAfterSeconds ?? 60) / 60));
            setError(t("web.auth.quickConnect.pinLockout", { count: minutes }));
            return null;
          }
          throw err;
        });
        if (!verified) return;
        if (!verified.valid || !verified.profile_token) {
          setError(t("web.auth.quickConnect.wrongPin"));
          return;
        }
        profileToken = verified.profile_token;
      }
      const context = captureProfileRequestContextAs(selected.id, profileToken);
      if (!context) {
        setError(t("web.auth.quickConnect.sessionEnded"));
        return;
      }
      await v2("POST /api/v2/auth/device/approve-handoff", {
        body: { code },
        profileContext: context,
      });
      onApproved(selected.name);
    } catch (err) {
      if (err instanceof V2ProblemError && err.status === 429) {
        setError(tooManyCodesMessage(err.retryAfterSeconds));
      } else if (err instanceof V2ProblemError && [404, 409, 410].includes(err.status)) {
        onMoved();
      } else if (err instanceof V2ProblemError && err.problemType === "permission_denied") {
        setError(t("web.auth.quickConnect.impersonating"));
      } else {
        setError(t("web.auth.quickConnect.approveFailed"));
      }
    } finally {
      setPin("");
      setActing(false);
    }
  }

  if (isLoading) {
    return (
      <p className="text-muted-foreground text-sm">{t("web.auth.quickConnect.loadingProfiles")}</p>
    );
  }
  if (isError || profiles.length === 0) {
    return (
      <div className="space-y-3">
        <p className="text-sm" role="alert">
          {t("web.auth.quickConnect.loadFailed")}
        </p>
        <Button type="button" className="w-full" onClick={() => void refetch()}>
          {t("common.tryAgain")}
        </Button>
      </div>
    );
  }

  const disabled = acting || busy;
  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <Label id="quick-connect-profile">
          {deviceName
            ? t("web.auth.quickConnect.profileLabel", { device: deviceName })
            : t("web.auth.quickConnect.profileLabelUnknown")}
        </Label>
        <RadioGroup
          aria-labelledby="quick-connect-profile"
          value={selectedId ?? undefined}
          onValueChange={(id) => {
            setChosenId(id);
            setPin("");
            setError(null);
          }}
          disabled={disabled}
        >
          {profiles.map((profile) => (
            <RadioCardItem
              key={profile.id}
              value={profile.id}
              label={profile.name}
              hint={profile.has_pin ? t("web.auth.quickConnect.needsPin") : undefined}
            />
          ))}
        </RadioGroup>
      </div>
      {needsPin ? (
        <div className="space-y-2">
          <Label htmlFor="quick-connect-pin">
            {t("web.auth.quickConnect.pinLabel", { profile: selected?.name ?? "" })}
          </Label>
          <Input
            id="quick-connect-pin"
            type="password"
            inputMode="numeric"
            autoComplete="off"
            value={pin}
            onChange={(e) => setPin(e.target.value)}
            disabled={disabled}
          />
        </div>
      ) : null}
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <div className="space-y-3">
        <Button
          className="w-full"
          disabled={disabled || !selected || (needsPin && pin.trim() === "")}
          onClick={() => void approve()}
        >
          {acting ? t("web.auth.login.signingIn") : t("web.auth.signIn")}
        </Button>
        <Button variant="outline" className="w-full" disabled={disabled} onClick={onDeny}>
          {t("web.auth.activate.notNow")}
        </Button>
      </div>
    </div>
  );
}
