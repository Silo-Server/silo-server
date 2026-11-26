import { OpenInSiloSection } from "@/components/appKeys/OpenInSiloSection";
import { OwnAppKeysPanel } from "@/components/appKeys/OwnAppKeysPanel";
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
    return (
      <>
        <OpenInSiloSection />
        <p role="status">{t("web.settings.signedInSessions.unavailable")}</p>
        <div className="mt-10">
          <OwnAppKeysPanel />
        </div>
      </>
    );
  // Absent from a server that doesn't send it yet: other sessions stay manageable.
  return (
    <>
      <OpenInSiloSection />
      <LoginSessionsPanel manageOthers={capabilities.data.revoke_other_sessions !== false} />
      <div className="mt-10">
        <OwnAppKeysPanel />
      </div>
    </>
  );
}
