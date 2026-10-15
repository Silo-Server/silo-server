package noderecipe

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const restoreAdmission = 2 * time.Second

// Restoration is an immutable, process-local capture. It carries neither a
// native publication descriptor nor authority beyond its predecessor.
type Restoration interface {
	ExpiresAt() time.Time
	Restore(context.Context, time.Time) error
}

type restoration struct {
	store      *Store
	work       string
	card       playback.RecipeCard
	expiry     time.Time
	generation string
}

func (r *restoration) ExpiresAt() time.Time { return r.expiry }

func (r *restoration) Restore(ctx context.Context, notAfter time.Time) error {
	if notAfter.IsZero() || notAfter.After(r.expiry) {
		notAfter = r.expiry
	}
	return r.store.restore(ctx, r.work, r.card, notAfter, r.generation)
}

var captureRestoreExpiryScript = redis.NewScript(`
local raw=redis.call('GET',KEYS[1])
local ttl=redis.call('PTTL',KEYS[1])
if not raw or ttl==0 or ttl==-2 then return redis.error_reply('recipe predecessor missing') end
if redis.call('EXISTS',KEYS[5])~=0 then return redis.error_reply('recipe predecessor publication pending') end
local now=redis.call('TIME')
local now_ms=now[1]*1000+math.floor(now[2]/1000)
local lifetime=ttl
if lifetime==-1 then lifetime=tonumber(ARGV[1]) end
for i=2,3 do
 local sidecar_ttl=redis.call('PTTL',KEYS[i])
 if sidecar_ttl>=0 then lifetime=math.min(lifetime,sidecar_ttl) end
end
local generation=''
if KEYS[4]~='' then generation=redis.call('GET',KEYS[4]) or '0' end
return {raw,tostring(now_ms+lifetime),generation,redis.call('GET',KEYS[2]) or '',redis.call('GET',KEYS[3]) or '',tostring(now_ms),tostring(ttl)}`)

// CaptureRestore binds the exact predecessor, its existing authority epoch,
// and its shortest absolute expiry before a candidate stop removes the record.
func (s *Store) CaptureRestore(ctx context.Context, work string, expected playback.RecipeCard) (Restoration, error) {
	if !s.Enabled() || s.prefix != KeyPrefix || work == "" {
		return nil, ErrPublicationUncertain
	}
	values, err := captureRestoreExpiryScript.Run(ctx, s.rdb, []string{s.key(work), nodeAuthorityRecordGenerationKey(work), nodeAuthorityRecordDigestKey(work), nodeAuthorityGenerationKeyForCard(expected), publicationOwnerKeyPrefix + work}, max(s.ttl.Milliseconds(), 1)).StringSlice()
	if err != nil {
		return nil, err
	}
	if len(values) != 7 || values[0] == "" {
		return nil, ErrPublicationUncertain
	}
	current, valid := unmarshalCard([]byte(values[0]))
	if !valid {
		return nil, ErrPublicationUncertain
	}
	want, wantErr := marshalCard(expected)
	got, gotErr := marshalCard(current)
	if wantErr != nil || gotErr != nil || string(want) != string(got) {
		return nil, ErrPublicationUncertain
	}
	expiry, err := strconv.ParseInt(values[1], 10, 64)
	if err != nil {
		return nil, err
	}
	snapshotTime, err := strconv.ParseInt(values[5], 10, 64)
	if err != nil || expiry <= snapshotTime {
		return nil, ErrPublicationUncertain
	}
	if values[2] != "" {
		generation, generationErr := strconv.ParseInt(values[2], 10, 64)
		if generationErr != nil {
			return nil, generationErr
		}
		if values[4] == nodeAuthorityRecordDigest([]byte(values[0])) {
			if values[3] != values[2] {
				return nil, ErrPublicationUncertain
			}
		} else if generation != 0 {
			// Match the existing legacy reader's issuance-after-revocation rule,
			// using the same atomic snapshot rather than blessing the new epoch.
			ttl, ttlErr := strconv.ParseInt(values[6], 10, 64)
			if ttlErr != nil || ttl < 0 || snapshotTime-(s.ttl.Milliseconds()-ttl) <= generation {
				return nil, ErrPublicationUncertain
			}
		}
	}
	return &restoration{store: s, work: work, card: current, expiry: time.UnixMilli(expiry), generation: values[2]}, nil
}

// Restore runs while its caller owns the exact live predecessor row and local
// lifecycle. It never leaves an already-issued command admissible after that
// ownership ends: uncertainty must retire its receipt or outlast its fixed
// admission deadline, even when the caller cancels in the meantime.
func (s *Store) restore(ctx context.Context, work string, card playback.RecipeCard, notAfter time.Time, generation string) error {
	if !s.Enabled() || s.prefix != KeyPrefix || work == "" || notAfter.IsZero() {
		return ErrPublicationUncertain
	}
	data, err := marshalCard(card)
	if err != nil {
		return err
	}
	publication, err := s.publishWithAdmission(ctx, work, card, data, s.ttl, true, restoreAdmission, notAfter, generation)
	if err == nil {
		// Acknowledged publication is already idempotent. A lost commit reply
		// cannot authorize removing this accepted restoration.
		if commitErr := publication.Commit(ctx); commitErr != nil {
			slog.WarnContext(ctx, "accepted recipe restoration acknowledgement pending", "error", commitErr)
		}
		return nil
	}
	if publication == nil || publication.prepareDeadline == 0 {
		return err // No Prepare or Publish could have been issued.
	}
	remaining := time.Until(publication.admissibleUntil)
	if remaining > 0 {
		retireCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), remaining)
		retired, retireErr := retireRecipePublicationScript.Run(retireCtx, s.rdb, publication.keys(), publication.version, publication.prepareDeadline).Int()
		cancel()
		if retireErr == nil && retired == 1 {
			return err
		}
		// Do not shorten this wait to the canceled callback context: a packet
		// already on the wire still carries its original server deadline.
		if remaining = time.Until(publication.admissibleUntil); remaining > 0 {
			timer := time.NewTimer(remaining)
			<-timer.C
		}
		return errors.Join(err, retireErr, ErrPublicationUncertain)
	}
	return err
}
