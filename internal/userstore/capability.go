package userstore

// Capability finds an optional store interface without losing it at a decorator.
// The outermost implementation wins so mutation hooks still intercept writes.
// Only transparent decorators should expose UnwrapUserStore.
func Capability[T any](store UserStore) (T, bool) {
	for store != nil {
		if capability, ok := any(store).(T); ok {
			return capability, true
		}
		wrapper, ok := store.(interface{ UnwrapUserStore() UserStore })
		if !ok {
			break
		}
		store = wrapper.UnwrapUserStore()
	}
	var zero T
	return zero, false
}
