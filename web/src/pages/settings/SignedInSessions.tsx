import { LoginSessionsPanel } from "@/components/sessions/LoginSessionsPanel";
import { Button } from "@/components/ui/button";
import { useLoginSessionCapabilities } from "@/hooks/queries/loginSessions";
import { useT } from "@/i18n";

export default function SignedInSessions() {
  const t = useT();
  const capabilities = useLoginSessionCapabilities();
  if (capabilities.isPending)
    return <p role="status">{t("web.settings.signedInSessions.loading")}</p>;
  if (capabilities.isError)
    return (
      <div role="alert" className="space-y-3">
        <p>{t("web.settings.signedInSessions.loadFailed")}</p>
        <Button onClick={() => void capabilities.refetch()}>{t("common.retry")}</Button>
      </div>
    );
  if (!capabilities.data?.available)
    return <p role="status">{t("web.settings.signedInSessions.unavailable")}</p>;
  return <LoginSessionsPanel />;
}
