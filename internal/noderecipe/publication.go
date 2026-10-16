package noderecipe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Publication owns compensation for exactly one write. Undo restores only its
// still-current candidate. Commit records durable acceptance and discards prior
// bytes. Separate receipt-bearing publications for one work cannot overlap.
type Publication interface {
	ID() string
	Undo(context.Context) error
	Retire(context.Context) error
	Commit(context.Context) error
}

// AcceptedPublication is authoritative evidence supplied while the caller owns
// its cross-replica mutation lock. Nil evidence never authorizes recovery.
type AcceptedPublication struct {
	Scope   string
	ID      string
	Card    *playback.RecipeCard
	Started bool
}

var ErrPublicationUncertain = errors.New("node recipe publication cannot be reconciled from authoritative state")

var ErrPublicationBusy = errors.New("node recipe publication is already in progress")
var ErrPublicationLost = errors.New("node recipe publication ownership expired or was superseded")

const publicationUndoKeyPrefix = "silo:noderecipe-undo:"
const publicationOwnerKeyPrefix = "silo:noderecipe-owner:"
const publicationPrepareTTL = 2 * time.Minute

type publication struct {
	store           *Store
	work, version   string
	prepareDeadline int64
	admissibleUntil time.Time
}

func (p *publication) ID() string {
	if p == nil {
		return ""
	}
	return p.version
}
func (p *publication) undoKey() string  { return publicationUndoKeyPrefix + p.version }
func (p *publication) ownerKey() string { return publicationOwnerKeyPrefix + p.work }
func (p *publication) keys() []string {
	return []string{p.store.key(p.work), publicationVersionKey(p.work), nodeAuthorityRecordGenerationKey(p.work), nodeAuthorityRecordDigestKey(p.work), p.undoKey(), p.ownerKey()}
}

// Both compensation and expired-owner recovery restore the exact bytes and
// absolute expiries captured by the publication. Neither grants a new epoch.
const restorePublicationLua = `
local function restore(previous,version)
 local now=redis.call('TIME')
 local now_ms=now[1]*1000+math.floor(now[2]/1000)
 redis.call('DEL',KEYS[1],KEYS[2],KEYS[3],KEYS[4])
 if not previous[1][1] or (previous[1][2]>=0 and previous[1][2]<=now_ms) then return end
 for i=1,4 do
  if i~=2 and previous[i][1] then
   if previous[i][2]==-1 then redis.call('SET',KEYS[i],previous[i][1])
   elseif previous[i][2]>now_ms then redis.call('SET',KEYS[i],previous[i][1],'PXAT',previous[i][2]) end
  end
 end
 if previous[1][2]==-1 then
  redis.call('SET',KEYS[2],version)
  redis.call('SET',ARGV[3]..version,'committed')
 else
  redis.call('SET',KEYS[2],version,'PXAT',previous[1][2])
  redis.call('SET',ARGV[3]..version,'committed','PXAT',previous[1][2])
 end
end
`

// Evidence binds the compat row, canonical work, exact encoded recipe and its
// accepted publication. A stale identifier on a cleared recipe is insufficient.
func acceptedPublicationEvidence(work string, accepted *AcceptedPublication) ([]any, error) {
	if accepted == nil {
		return []any{"0", "", "", "", "0", ""}, nil
	}
	var data []byte
	var err error
	if accepted.Card != nil {
		data, err = marshalCard(*accepted.Card)
		if err != nil {
			return nil, err
		}
	}
	started := "0"
	if accepted.Started {
		started = "1"
	}
	encoded, _ := json.Marshal([]string{"silo:accepted-node-recipe:v1", accepted.Scope, work, accepted.ID, string(data), started})
	digest := sha256.Sum256(encoded)
	return []any{"1", accepted.ID, accepted.Scope, string(data), started, hex.EncodeToString(digest[:])}, nil
}

// A terminal attempt remains unusable through its fixed admission deadline,
// including when a timed-out first Prepare arrives after a successful retry.
const terminalPublicationLua = `
local function terminal(key,deadline)
 local now=redis.call('TIME')
 local now_ms=now[1]*1000+math.floor(now[2]/1000)
 local expiry=tonumber(deadline) or 0
 local ttl=redis.call('PTTL',key)
 if ttl>0 then expiry=math.max(expiry,now_ms+ttl) end
 if expiry>now_ms then redis.call('SET',key,'retired','PXAT',expiry)
 else redis.call('DEL',key) end
end
`

// A pre-receipt PutTTL token and a modern token with lost metadata have the
// same format. Fresh locked legacy-row evidence may reconstruct the exact
// accepted bytes under a new token, but never commits the unknown old token.
// Preserve every authority expiry and retire the old admission for the maximum
// preparation window, so delayed original commands cannot undo this recovery.
const reconcilePublicationLua = restorePublicationLua + terminalPublicationLua + `
local function rebuild_legacy(current,owner,receipt_key)
 if owner or not current or #current~=32 or string.find(current,'[^0-9a-f]') then return 0 end
 if ARGV[5]~='1' or ARGV[6]~='' or ARGV[7]=='' or ARGV[8]=='' or ARGV[9]~='1' then return 0 end
 if redis.call('GET',KEYS[1])~=ARGV[8] then return 0 end
 local ttl=redis.call('PTTL',KEYS[2])
 if ttl~=-1 and ttl<=0 then return 0 end
 local now=redis.call('TIME')
 local now_ms=now[1]*1000+math.floor(now[2]/1000)
 terminal(receipt_key,now_ms+tonumber(ARGV[2]))
 redis.call('SET',KEYS[2],ARGV[4],'KEEPTTL')
 if ttl==-1 then redis.call('SET',ARGV[3]..ARGV[4],'committed')
 else redis.call('SET',ARGV[3]..ARGV[4],'committed','PX',ttl) end
 return 1
end
local function reconcile()
 local owner=redis.call('GET',KEYS[6])
 local current=redis.call('GET',KEYS[2])
 local pending=owner or current
 if not pending then return 1 end
 local receipt_key=ARGV[3]..pending
 local encoded=redis.call('GET',receipt_key)
 if not encoded then return rebuild_legacy(current,owner,receipt_key) end
 if encoded=='retired' then return 0 end
 if encoded=='committed' then
  if owner==pending then redis.call('DEL',KEYS[6]) end
  return 1
 end
 if ARGV[5]~='1' then return 0 end
 local receipt=cjson.decode(encoded)
 if receipt.scope~=ARGV[7] then return 0 end
 if pending==ARGV[6] and ARGV[9]=='1' and receipt.candidate==ARGV[8] then
  local ttl=redis.call('PTTL',receipt_key)
  if ttl<=0 then return 0 end
  redis.call('SET',receipt_key,'committed','PX',ttl)
 elseif receipt.predecessor==ARGV[10] then
  if current==pending and receipt.candidate and redis.call('GET',KEYS[1])==receipt.candidate then restore(receipt.previous,ARGV[4]) end
  terminal(receipt_key,receipt.deadline)
 else return 0 end
 if owner==pending then redis.call('DEL',KEYS[6]) end
 return 1
end
`

var prepareRecipePublicationScript = redis.NewScript(reconcilePublicationLua + `
local now=redis.call('TIME')
local now_ms=now[1]*1000+math.floor(now[2]/1000)
local deadline=tonumber(ARGV[11])
if not deadline or deadline<=now_ms then return 0 end
local prior=redis.call('GET',KEYS[5])
local owner=redis.call('GET',KEYS[6])
if prior then
 if prior=='committed' or prior=='retired' then return 0 end
 if owner==ARGV[1] then return 1 end
 return 0
end
if reconcile()~=1 then return 0 end
redis.call('SET',KEYS[6],ARGV[1],'PXAT',deadline)
redis.call('SET',KEYS[5],cjson.encode({state='prepared',scope=ARGV[7],predecessor=ARGV[10],deadline=deadline}),'PXAT',deadline)
return 1`)

var reconcileAcceptedPublicationScript = redis.NewScript(reconcilePublicationLua + `return reconcile()`)

// ReconcileAccepted must run under the same authoritative mutation lock used
// for publication and database commit. Unknown evidence changes neither record.
func (s *Store) ReconcileAccepted(ctx context.Context, work string, accepted *AcceptedPublication) error {
	if s == nil || s.rdb == nil || work == "" {
		return nil
	}
	if s.prefix != KeyPrefix {
		return errors.New("publication reconciliation requires node recipe store")
	}
	evidence, err := acceptedPublicationEvidence(work, accepted)
	if err != nil {
		return err
	}
	fresh, err := publicationVersion()
	if err != nil {
		return err
	}
	receipt := &publication{store: s, work: work}
	args := append([]any{"", publicationPrepareTTL.Milliseconds(), publicationUndoKeyPrefix, fresh}, evidence...)
	reconciled, err := reconcileAcceptedPublicationScript.Run(ctx, s.rdb, receipt.keys(), args...).Int()
	if err != nil {
		return err
	}
	if reconciled != 1 {
		return ErrPublicationUncertain
	}
	return nil
}

// Acknowledged preparation is required before sending this script. A delayed
// write cannot arrive after cancellation/expiry and recreate failed authority.
var publishRecipeScript = redis.NewScript(`
local ttl=tonumber(ARGV[2])
local not_after=tonumber(ARGV[7]) or 0
if ARGV[8] and ARGV[8]~='' and (KEYS[7]=='' or (redis.call('GET',KEYS[7]) or '0')~=ARGV[8]) then
 return redis.error_reply('recipe predecessor authority revoked')
end
if not_after>0 then
 local now=redis.call('TIME')
 ttl=math.min(ttl,not_after-now[1]*1000-math.floor(now[2]/1000))
 if ttl<=0 then return redis.error_reply('recipe restoration expired') end
end
if ARGV[5]=='1' then
 if redis.call('GET',KEYS[6])~=ARGV[4] then return redis.error_reply('recipe publication owner expired') end
 local receipt=redis.call('GET',KEYS[5])
 if not receipt then return redis.error_reply('recipe publication window expired') end
 if receipt=='committed' or receipt=='retired' then return redis.error_reply('recipe publication is terminal') end
 local prepared=cjson.decode(receipt)
 local now=redis.call('TIME')
 local now_ms=now[1]*1000+math.floor(now[2]/1000)
 if not prepared.deadline or prepared.deadline<=now_ms then return redis.error_reply('recipe publication window expired') end
 if prepared.state~='prepared' then return 1 end
 local previous={}
 for i=1,4 do
  local value=redis.call('GET',KEYS[i])
  local ttl=redis.call('PTTL',KEYS[i])
  local expiry=ttl
  if ttl>=0 then expiry=now_ms+ttl end
  previous[i]={value,expiry}
 end
 redis.call('SET',KEYS[5],cjson.encode({state='published',candidate=ARGV[1],previous=previous,scope=prepared.scope,predecessor=prepared.predecessor,deadline=prepared.deadline}),'PXAT',math.max(now_ms+ttl,prepared.deadline))
 redis.call('PEXPIREAT',KEYS[6],prepared.deadline)
end
local generation=false
if KEYS[7]~='' then generation=redis.call('GET',KEYS[7]) or '0' end
redis.call('SET',KEYS[1],ARGV[1],'PX',ttl)
redis.call('SET',KEYS[2],ARGV[4],'PX',ttl)
if generation then
 redis.call('SET',KEYS[3],generation,'PX',ttl)
 redis.call('SET',KEYS[4],ARGV[3],'PX',ttl)
else redis.call('DEL',KEYS[3],KEYS[4]) end
if ARGV[5]~='1' then redis.call('SET',KEYS[5],'committed','PX',ttl) end
return 1`)

var undoRecipePublicationScript = redis.NewScript(restorePublicationLua + terminalPublicationLua + `
local encoded=redis.call('GET',KEYS[5])
if encoded=='committed' or encoded=='retired' then return 0 end
if redis.call('GET',KEYS[2])==ARGV[1] then
 if not encoded then return redis.error_reply('recipe publication undo record expired') end
 local receipt=cjson.decode(encoded)
 if redis.call('GET',KEYS[1])==receipt.candidate then restore(receipt.previous,ARGV[2]) end
end
terminal(KEYS[5],ARGV[4])
if redis.call('GET',KEYS[6])==ARGV[1] then redis.call('DEL',KEYS[6]) end
return 1`)

// Confirmed rejection can retire the exact candidate after its authoritative
// predecessor changed. It must never put that predecessor back into service.
var retireRecipePublicationScript = redis.NewScript(terminalPublicationLua + `
local encoded=redis.call('GET',KEYS[5])
if encoded=='committed' then return 0 end
if encoded=='retired' then return 1 end
if redis.call('GET',KEYS[2])==ARGV[1] then
 if not encoded then return redis.error_reply('recipe publication retirement record expired') end
 local receipt=cjson.decode(encoded)
 if receipt.candidate and redis.call('GET',KEYS[1])==receipt.candidate then redis.call('DEL',KEYS[1],KEYS[2],KEYS[3],KEYS[4]) end
end
terminal(KEYS[5],ARGV[2])
if redis.call('GET',KEYS[6])==ARGV[1] then redis.call('DEL',KEYS[6]) end
return 1`)

var commitRecipePublicationScript = redis.NewScript(`
local receipt=redis.call('GET',KEYS[5])
if receipt=='committed' then return 1 end
if redis.call('GET',KEYS[6])~=ARGV[1] or redis.call('GET',KEYS[2])~=ARGV[1] or not receipt then return 0 end
local ttl=redis.call('PTTL',KEYS[5])
if ttl<=0 then return 0 end
redis.call('SET',KEYS[5],'committed','PX',ttl)
redis.call('DEL',KEYS[6])
return 1`)

// PublishWithUndo returns compensation even on a write error, since Redis may
// commit before its response is lost. A concurrent publication returns Busy
// without changing the existing owner's recipe. After owner expiry the next
// preparation requires authoritative evidence to resolve the prior candidate.
func (s *Store) PublishWithUndo(ctx context.Context, work string, card playback.RecipeCard, accepted ...*AcceptedPublication) (Publication, error) {
	if s == nil || s.rdb == nil || work == "" {
		return (*publication)(nil), nil
	}
	if s.prefix != KeyPrefix {
		return nil, errors.New("publication undo requires node recipe store")
	}
	data, err := marshalCard(card)
	if err != nil {
		return nil, err
	}
	return s.publish(ctx, work, card, data, s.ttl, true, accepted...)
}
func (s *Store) publish(ctx context.Context, work string, card playback.RecipeCard, data []byte, ttl time.Duration, undo bool, accepted ...*AcceptedPublication) (*publication, error) {
	return s.publishWithAdmission(ctx, work, card, data, ttl, undo, publicationPrepareTTL, time.Time{}, "", accepted...)
}

//nolint:nestif // Reconciliation must keep candidate identity, undo ownership and acknowledgement outcomes in one ordered decision.
func (s *Store) publishWithAdmission(ctx context.Context, work string, card playback.RecipeCard, data []byte, ttl time.Duration, undo bool, admission time.Duration, notAfter time.Time, generation string, accepted ...*AcceptedPublication) (*publication, error) {
	version, err := publicationVersion()
	if err != nil {
		return nil, err
	}
	receipt := &publication{store: s, work: work, version: version}
	capture := "0"
	if undo {
		restoredVersion, err := publicationVersion()
		if err != nil {
			return nil, err
		}
		var previous *AcceptedPublication
		if len(accepted) > 0 {
			previous = accepted[0]
		}
		evidence, err := acceptedPublicationEvidence(work, previous)
		if err != nil {
			return nil, err
		}
		// Acknowledged server time fixes the one admission window before any
		// Prepare write is sent. Retries keep this deadline permanently.
		serverTime, timeErr := s.rdb.Time(ctx).Result()
		if timeErr != nil {
			return receipt, timeErr
		}
		if !notAfter.IsZero() {
			admission = min(admission, notAfter.Sub(serverTime))
			if deadline, ok := ctx.Deadline(); ok {
				admission = min(admission, time.Until(deadline))
			}
			if admission <= 0 || ctx.Err() != nil {
				return receipt, context.DeadlineExceeded
			}
		}
		receipt.prepareDeadline = serverTime.Add(admission).UnixMilli()
		// A local monotonic wait starting after the acknowledged TIME reply
		// conservatively outlasts this server's fixed admission deadline.
		receipt.admissibleUntil = time.Now().Add(admission)
		args := append([]any{version, publicationPrepareTTL.Milliseconds(), publicationUndoKeyPrefix, restoredVersion}, evidence...)
		args = append(args, receipt.prepareDeadline)
		prepared, err := prepareRecipePublicationScript.Run(ctx, s.rdb, receipt.keys(), args...).Int()
		if err != nil {
			return receipt, err
		}
		if prepared != 1 {
			if previous == nil {
				return receipt, ErrPublicationBusy
			}
			return receipt, ErrPublicationUncertain
		}
		capture = "1"
	}
	keys := append(receipt.keys(), nodeAuthorityGenerationKeyForCard(card))
	var absoluteExpiry int64
	if !notAfter.IsZero() {
		absoluteExpiry = notAfter.UnixMilli()
	}
	err = publishRecipeScript.Run(ctx, s.rdb, keys, data, max(ttl.Milliseconds(), 1), nodeAuthorityRecordDigest(data), version, capture, publicationPrepareTTL.Milliseconds(), absoluteExpiry, generation).Err()
	return receipt, err
}
func (p *publication) Undo(ctx context.Context) error {
	if p == nil {
		return nil
	}
	version, err := publicationVersion()
	if err != nil {
		return err
	}
	return undoRecipePublicationScript.Run(ctx, p.store.rdb, p.keys(), p.version, version, publicationUndoKeyPrefix, p.prepareDeadline).Err()
}

// Retire removes only this still-current candidate, without restoring prior
// authority. Use it only after authoritative confirmation of rejection.
func (p *publication) Retire(ctx context.Context) error {
	if p == nil {
		return nil
	}
	retired, err := retireRecipePublicationScript.Run(ctx, p.store.rdb, p.keys(), p.version, p.prepareDeadline).Int()
	if err != nil {
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		retired, err = retireRecipePublicationScript.Run(retryCtx, p.store.rdb, p.keys(), p.version, p.prepareDeadline).Int()
	}
	if err != nil {
		return err
	}
	if retired != 1 {
		return ErrPublicationLost
	}
	return nil
}
func (p *publication) Commit(ctx context.Context) error {
	if p == nil {
		return nil
	}
	committed, err := commitRecipePublicationScript.Run(ctx, p.store.rdb, p.keys(), p.version, p.prepareDeadline).Int()
	if err != nil {
		// Commit is idempotent. Its compact marker survives a lost response, so
		// ownership loss is distinguished from an already acknowledged commit.
		verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		committed, err = commitRecipePublicationScript.Run(verifyCtx, p.store.rdb, p.keys(), p.version, p.prepareDeadline).Int()
	}
	if err != nil {
		return err
	}
	if committed != 1 {
		return ErrPublicationLost
	}
	return nil
}
