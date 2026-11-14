package policy

import (
	"encoding/json"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// decisionMemoEntries bounds how many results one compiled bundle remembers;
// it sets how many concurrent viewers share the saving.
const decisionMemoEntries = 4096

var decisionMemoLookups = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_policy_decision_memo_lookups_total", Help: "Policy decision memo lookups by decision and result.",
}, []string{"decision", "result"})

// decisionMemo remembers the encoded results of one compiled bundle by
// decision name and JSON input, evicting the least recently used past its
// limit. A result is a pure function of those two: LockedCapabilities drops
// every builtin OPA marks nondeterministic, the engine attaches no data
// document, and OPA converts the input through the same JSON encoding. The
// key leaves out only request fields the bundle provably never reads. swap
// installs a new memo with every bundle, so no result outlives the bundle
// that produced it. It is safe for concurrent use.
type decisionMemo struct {
	limit  int
	unread unreadRequestFields

	mu      sync.Mutex
	entries map[decisionMemoKey]*decisionMemoEntry
	// lru is the sentinel of a ring of the entries: lru.next is the most
	// recently used, lru.prev the least.
	lru decisionMemoEntry
}

type decisionMemoKey struct {
	name  DecisionName
	input string
}

type decisionMemoEntry struct {
	key        decisionMemoKey
	raw        []byte
	prev, next *decisionMemoEntry
}

func newDecisionMemo(limit int, unread unreadRequestFields) *decisionMemo {
	m := &decisionMemo{
		limit:   limit,
		unread:  unread,
		entries: make(map[decisionMemoKey]*decisionMemoEntry),
	}
	m.lru.prev, m.lru.next = &m.lru, &m.lru
	return m
}

// unreadRequestFields marks the per-request input fields that no rule in a
// bundle can read (see bundleInputKeys). request_time changes every second,
// and device_id and client_ip differ between a viewer's clients, so keying on
// them would split one viewer's identical decisions across entries.
type unreadRequestFields struct {
	requestTime, deviceID, clientIP bool
}

func unreadRequestFieldsOf(modules []ModuleSource) unreadRequestFields {
	keys, whole := bundleInputKeys(modules)
	if whole {
		return unreadRequestFields{}
	}
	return unreadRequestFields{
		requestTime: !keys["request_time"],
		deviceID:    !keys["device_id"],
		clientIP:    !keys["client_ip"],
	}
}

// clear returns input with its unread request fields emptied. Only the typed
// inputs are cleared; any other input is keyed whole.
func (u unreadRequestFields) clear(input any) any {
	switch v := input.(type) {
	case ScopeInput:
		u.clearFields(&v.RequestTime, &v.DeviceID, &v.ClientIP)
		return v
	case PermissionInput:
		u.clearFields(&v.RequestTime, &v.DeviceID, &v.ClientIP)
		return v
	case ActionInput:
		u.clearFields(&v.RequestTime, &v.DeviceID, &v.ClientIP)
		return v
	}
	return input
}

func (u unreadRequestFields) clearFields(requestTime, deviceID, clientIP *string) {
	if u.requestTime {
		*requestTime = ""
	}
	if u.deviceID {
		*deviceID = ""
	}
	if u.clientIP {
		*clientIP = ""
	}
}

// key keys a decision by its input's JSON encoding, leaving out the request
// fields the bundle cannot read. An input that does not encode is not
// remembered.
func (m *decisionMemo) key(name DecisionName, input any) (decisionMemoKey, bool) {
	if m == nil {
		return decisionMemoKey{}, false
	}
	encoded, err := json.Marshal(m.unread.clear(input))
	if err != nil {
		return decisionMemoKey{}, false
	}
	return decisionMemoKey{name: name, input: string(encoded)}, true
}

// get returns the encoded result remembered for key. Callers must not modify
// it.
func (m *decisionMemo) get(key decisionMemoKey) ([]byte, bool) {
	if m == nil {
		return nil, false
	}
	var raw []byte
	m.mu.Lock()
	entry, ok := m.entries[key]
	if ok {
		m.unlinkLocked(entry)
		m.pushFrontLocked(entry)
		raw = entry.raw
	}
	m.mu.Unlock()
	result := "miss"
	if ok {
		result = "hit"
	}
	decisionMemoLookups.WithLabelValues(string(key.name), result).Inc()
	return raw, ok
}

func (m *decisionMemo) put(key decisionMemoKey, raw []byte) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; ok {
		return
	}
	entry := &decisionMemoEntry{key: key, raw: raw}
	m.pushFrontLocked(entry)
	m.entries[key] = entry
	if len(m.entries) > m.limit {
		oldest := m.lru.prev
		m.unlinkLocked(oldest)
		delete(m.entries, oldest.key)
	}
}

func (m *decisionMemo) unlinkLocked(entry *decisionMemoEntry) {
	entry.prev.next, entry.next.prev = entry.next, entry.prev
}

func (m *decisionMemo) pushFrontLocked(entry *decisionMemoEntry) {
	entry.prev, entry.next = &m.lru, m.lru.next
	m.lru.next.prev = entry
	m.lru.next = entry
}
