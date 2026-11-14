import { useRef, useState } from "react";
import { Loader2, Send } from "lucide-react";
import { toast } from "sonner";
import { invalidEmailMessage, isValidEmail } from "@/lib/email";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import { v2, type V2Result } from "@/api/v2/request";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useT } from "@/i18n";

/** Sends a real message through the saved SMTP settings. */
export function TestEmailRow() {
  const t = useT();
  const [recipient, setRecipient] = useState("");
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<V2Result<"POST /api/v2/admin/email/test"> | null>(null);

  const inFlight = useRef(false);
  const sendTest = async () => {
    if (inFlight.current) return;
    const profileContext = captureProfileRequestContext();
    if (!profileContext) {
      toast.error(t("web.admin.common.emailTest.selectProfile"));
      return;
    }
    const to = recipient.trim();
    if (!isValidEmail(to)) {
      toast.error(invalidEmailMessage());
      return;
    }
    inFlight.current = true;
    setPending(true);
    setResult(null);
    try {
      const response = await v2("POST /api/v2/admin/email/test", {
        body: { to },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext)) return;
      setResult(response);
      if (response.ok) {
        toast.success(t("web.admin.common.emailTest.sent"));
      }
    } catch (error) {
      if (!isCapturedProfileAuthorityActive(profileContext)) return;
      toast.error(
        error instanceof Error ? error.message : t("web.admin.common.emailTest.requestFailed"),
      );
    } finally {
      inFlight.current = false;
      setPending(false);
    }
  };

  return (
    <div className="space-y-2 py-3">
      <div className="flex max-w-md gap-2">
        <Input
          type="email"
          aria-label={t("web.admin.common.emailTest.recipientLabel")}
          placeholder={t("web.admin.common.emailTest.recipientPlaceholder")}
          value={recipient}
          onChange={(event) => setRecipient(event.target.value)}
        />
        <Button
          variant="outline"
          disabled={pending || !recipient.trim()}
          onClick={() => void sendTest()}
        >
          {pending ? (
            <Loader2 className="me-1.5 h-4 w-4 animate-spin" />
          ) : (
            <Send className="me-1.5 h-4 w-4" />
          )}
          {t("web.admin.common.emailTest.send")}
        </Button>
      </div>
      {result && (
        <p className={`text-xs ${result.ok ? "text-emerald-500" : "text-amber-500"}`}>
          {result.ok
            ? t("web.admin.common.emailTest.delivered", { duration_ms: result.duration_ms })
            : result.message || t("web.admin.common.emailTest.failed")}
        </p>
      )}
      <p className="text-muted-foreground text-xs">{t("web.admin.common.emailTest.saveFirst")}</p>
    </div>
  );
}
