package queryattr

import (
	"fmt"
	"hash/fnv"
	"hash/maphash"
	"sync"
	"sync/atomic"

	"github.com/DataDog/go-sqllexer"
)

var shapeCache sync.Map
var shapeCacheSize atomic.Uint64
var shapeSeed = maphash.MakeSeed()
var obfuscator = sqllexer.NewObfuscator(sqllexer.WithReplacePositionalParameter(true))
var normalizer = sqllexer.NewNormalizer()

type shapeKey struct {
	site    *site
	sqlHash uint64
}

// SQL and normalized text remain local to this function. Neither enters a cache or output.
func queryRow(s *site, sql string) *row {
	key := shapeKey{s, maphash.String(shapeSeed, sql)}
	if cached, ok := shapeCache.Load(key); ok {
		r, _ := cached.(*row)
		return r
	}
	normalized, _, err := sqllexer.ObfuscateAndNormalize(sql, obfuscator, normalizer, sqllexer.WithDBMS(sqllexer.DBMSPostgres))
	var r *row
	if err == nil {
		h := fnv.New64a()
		_, _ = h.Write([]byte(normalized))
		r = s.hashedRow(h.Sum64())
	} else {
		r = s.shapeRow(shapeOther)
	}
	actual, loaded := shapeCache.LoadOrStore(key, r)
	if !loaded && shapeCacheSize.Add(1) >= 4096 {
		shapeCache.Clear()
		shapeCacheSize.Store(0)
	}
	stored, _ := actual.(*row)
	return stored
}

func (s *site) hashedRow(hash uint64) *row {
	s.mu.Lock()
	defer s.mu.Unlock()
	// At most eight shapes are scanned; formatting happens only when a row is created.
	for _, r := range s.rows {
		if len(r.shape) == 16 && r.hash == hash {
			return r
		}
	}
	count := len(s.rows)
	if s.rows[shapeOther] != nil {
		count--
	}
	if count >= 8 {
		return s.shapeRowLocked(shapeOther)
	}
	r := s.shapeRowLocked(fmt.Sprintf("%016x", hash))
	r.hash = hash
	return r
}
