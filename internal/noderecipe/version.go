package noderecipe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const publicationVersionKeyPrefix = "silo:noderecipe-publication:"

func publicationVersionKey(work string) string { return publicationVersionKeyPrefix + work }
func publicationVersion() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

var putPublicationScript = redis.NewScript(`
redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[2])
redis.call('SET',KEYS[2],ARGV[3],'PX',ARGV[2])
redis.call('DEL',KEYS[3],KEYS[4])
return 1`)

// Older records have no publication token. Adopt the exact record under one
// Redis operation; subsequent current Put calls always replace that token,
// including byte-identical cards. The sidecar keeps the legacy JSON unchanged.
var capturePublicationScript = redis.NewScript(`
local recipe=redis.call('GET',KEYS[1])
if not recipe then return {'',''} end
local version=redis.call('GET',KEYS[2])
if not version then
 local ttl=redis.call('PTTL',KEYS[1])
 if ttl==-2 or ttl==0 then return {'',''} end
 if ttl<0 then ttl=tonumber(ARGV[2]) end
 redis.call('SET',KEYS[2],ARGV[1],'PX',ttl)
 version=ARGV[1]
end
return {recipe,version}`)

var deletePublicationScript = redis.NewScript(`
if redis.call('GET',KEYS[2])~=ARGV[1] then return 0 end
redis.call('DEL',KEYS[1],KEYS[2],KEYS[3],KEYS[4])
return 1`)

// RecipeVersion captures the card and unique publication token atomically.
// A later write has a new token even when every recipe byte is unchanged.
func (s *Store) RecipeVersion(ctx context.Context, work string) (*playback.RecipeCard, string, error) {
	if s == nil || s.rdb == nil || work == "" {
		return nil, "", nil
	}
	if s.prefix != KeyPrefix {
		return nil, "", errors.New("publication versions require node recipe store")
	}
	version, err := publicationVersion()
	if err != nil {
		return nil, "", err
	}
	values, err := capturePublicationScript.Run(ctx, s.rdb, []string{s.key(work), publicationVersionKey(work)}, version, max(s.ttl.Milliseconds(), 1)).StringSlice()
	if err != nil {
		return nil, "", err
	}
	if len(values) != 2 {
		return nil, "", errors.New("invalid recipe publication")
	}
	if values[0] == "" {
		return nil, "", nil
	}
	card, valid := unmarshalCard([]byte(values[0]))
	if !valid || !validPublicationVersion(values[1]) {
		return nil, "", errors.New("invalid recipe publication")
	}
	return &card, values[1], nil
}

func validPublicationVersion(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}

// DeleteVersion deletes only the captured publication and its sidecars. A
// mismatch/missing publication is already retired; an empty token never deletes.
func (s *Store) DeleteVersion(ctx context.Context, work, version string) error {
	if s == nil || s.rdb == nil || work == "" || version == "" {
		return nil
	}
	if s.prefix != KeyPrefix || !validPublicationVersion(version) {
		return errors.New("invalid node recipe publication version")
	}
	return deletePublicationScript.Run(ctx, s.rdb, []string{s.key(work), publicationVersionKey(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work)}, version).Err()
}
