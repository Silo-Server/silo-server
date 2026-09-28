export const PUSH_RELAY_SOURCE_URL = "https://github.com/Silo-Server/silo-push-relay";

export function MobilePushPrivacyDisclosure() {
  return (
    <div className="space-y-2 py-3">
      <div className="text-sm font-medium">Privacy disclosure</div>
      <div className="text-muted-foreground space-y-2 text-xs leading-relaxed">
        <p>
          When push notifications are enabled, your Silo Server sends a content-free request to
          Silo's push relay so Silo can deliver notifications through Apple Push Notification
          service or Firebase Cloud Messaging. The relay is{" "}
          <a
            href={PUSH_RELAY_SOURCE_URL}
            target="_blank"
            rel="noreferrer"
            className="text-foreground underline underline-offset-2"
          >
            fully open source
          </a>
          .
        </p>
        <p>
          The relay does not receive notification titles, message bodies, media names, user names,
          profile names, or your server URL. It does process technical metadata needed to deliver
          and operate the service, including an opaque deployment identifier, push delivery timing,
          request status, app topic, the IP address your self-hosted Silo Server uses to contact the
          relay, and a hashed device push token. Apple or Google may also process standard push
          delivery metadata for their platform.
        </p>
        <p>
          Push notifications are generic; the app fetches private content directly from your Silo
          Server after receiving the push.
        </p>
      </div>
    </div>
  );
}
