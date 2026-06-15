package notifications

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// serverChannelSender owns the transport for server-channel posts: ciphertext
// handling and the guarded HTTP client. It is shared by the sweep worker, the
// request-event path, and admin test sends.
type serverChannelSender struct {
	cipher   *secret.Cipher
	settings *Settings
	client   *http.Client
}

func newServerChannelSender(cipher *secret.Cipher, settings *Settings) *serverChannelSender {
	return &serverChannelSender{
		cipher:   cipher,
		settings: settings,
		client: newWebhookHTTPClient(func() bool {
			return settings.WebhooksAllowPrivateDestinations(context.Background())
		}),
	}
}

func (s *serverChannelSender) decryptURL(ch *ServerChannel) (string, error) {
	return s.cipher.Decrypt(ch.URLCiphertext, serverChannelURLAAD(ch.ID))
}

func (s *serverChannelSender) decryptSecret(ch *ServerChannel) (string, error) {
	if ch.SigningSecretCiphertext == nil {
		return "", fmt.Errorf("server channel has no signing secret")
	}
	return s.cipher.Decrypt(*ch.SigningSecretCiphertext, serverChannelSecretAAD(ch.ID))
}

// buildPayload renders the type-specific body and headers for one post:
// Discord bodies go unsigned, generic bodies get the signed Silo headers.
func (s *serverChannelSender) buildPayload(ch *ServerChannel, event string, buildDiscord, buildGeneric func() ([]byte, error)) (body []byte, headers map[string]string, err error) {
	switch ch.Type {
	case WebhookTypeDiscord:
		body, err = buildDiscord()
		return body, nil, err
	case WebhookTypeGeneric:
		body, err = buildGeneric()
		if err != nil {
			return nil, nil, err
		}
		signingSecret, err := s.decryptSecret(ch)
		if err != nil {
			return nil, nil, err
		}
		return body, serverChannelHeaders(event, ch.ID, signingSecret, time.Now(), body), nil
	default:
		return nil, nil, fmt.Errorf("unknown server channel type %q", ch.Type)
	}
}

// buildContent renders the type-specific body and headers for a content
// digest post.
func (s *serverChannelSender) buildContent(ch *ServerChannel, groups []ContentGroup, test bool) ([]byte, map[string]string, error) {
	return s.buildPayload(ch, ServerChannelEventContentAdded,
		func() ([]byte, error) { return BuildServerChannelDiscordContent(groups, test) },
		func() ([]byte, error) { return BuildServerChannelGenericContent(groups, ch.ID, test) })
}

// buildRequest renders the type-specific body and headers for one request
// lifecycle post.
func (s *serverChannelSender) buildRequest(ch *ServerChannel, event string, info RequestEventInfo) ([]byte, map[string]string, error) {
	return s.buildPayload(ch, event,
		func() ([]byte, error) { return BuildServerChannelRequestDiscord(event, info) },
		func() ([]byte, error) { return BuildServerChannelRequestGeneric(event, info, ch.ID) })
}

// post decrypts the destination and POSTs one prepared payload.
func (s *serverChannelSender) post(ctx context.Context, ch *ServerChannel, body []byte, headers map[string]string) webhookSendResult {
	url, err := s.decryptURL(ch)
	if err != nil {
		return webhookSendResult{Message: "channel URL could not be decrypted"}
	}
	return sendWebhook(ctx, s.client, url, body, headers)
}

// sendContent renders and posts a content digest.
func (s *serverChannelSender) sendContent(ctx context.Context, ch *ServerChannel, groups []ContentGroup, test bool) webhookSendResult {
	body, headers, err := s.buildContent(ch, groups, test)
	if err != nil {
		return webhookSendResult{Message: "payload build failed"}
	}
	return s.post(ctx, ch, body, headers)
}

// sendRequest renders and posts one request lifecycle event.
func (s *serverChannelSender) sendRequest(ctx context.Context, ch *ServerChannel, event string, info RequestEventInfo) webhookSendResult {
	body, headers, err := s.buildRequest(ch, event, info)
	if err != nil {
		return webhookSendResult{Message: "payload build failed"}
	}
	return s.post(ctx, ch, body, headers)
}
