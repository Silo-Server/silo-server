import { useT } from "@/i18n";

export const PUSH_RELAY_SOURCE_URL = "https://github.com/Silo-Server/silo-push-relay";

export function MobilePushPrivacyDisclosure() {
  const t = useT();
  return (
    <div className="space-y-2 py-3">
      <div className="text-sm font-medium">{t("web.notifications.pushPrivacy.title")}</div>
      <div className="text-muted-foreground space-y-2 text-xs leading-relaxed">
        <p>
          {t("web.notifications.pushPrivacy.relay", {
            link: (chunks) => (
              <a
                href={PUSH_RELAY_SOURCE_URL}
                target="_blank"
                rel="noreferrer"
                className="text-foreground underline underline-offset-2"
              >
                {chunks}
              </a>
            ),
          })}
        </p>
        <p>{t("web.notifications.pushPrivacy.metadata")}</p>
        <p>{t("web.notifications.pushPrivacy.generic")}</p>
      </div>
    </div>
  );
}
