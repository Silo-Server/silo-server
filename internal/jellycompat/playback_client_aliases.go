package jellycompat

// ResolveDeviceClientPlaySessionID uses the request's explicit device identity
// to disambiguate a current play from older records that never stored a device.
// It does not merge or delete those records, or guess when identity is absent.
func (s *PlaybackSessionStore) ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID string, includeTerminal bool) (*PlaybackSession, error) {
	if deviceID != "" {
		if session, ok := s.findDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal); ok {
			return session, nil
		}
	}
	return nil, ErrSessionNotFound
}

func (d *DurableCompatPlaybackStore) ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID string, includeTerminal bool) (*PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal)
	}
	if token == "" || playID == "" || deviceID == "" {
		return nil, ErrSessionNotFound
	}
	cached, cachedOK := d.mem.findDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal)
	if cachedOK && !d.shouldRevalidateToken(token) {
		return cached, nil
	}
	if err := d.loadByCompatToken(token); err != nil {
		if cachedOK {
			return cached, nil
		}
		return nil, err
	}
	return d.mem.ResolveDeviceClientPlaySessionID(token, playID, deviceID, routeID, sourceID, includeTerminal)
}

// ResolveClientPlaySessionID preserves a storage failure for callers that may
// otherwise create a new static reservation after an ambiguous lookup.
//
// Records left by the old parallel Static=true creation race share a client
// play ID but never stored a device, so nothing proves they belong to one
// installation. They stay ambiguous (ErrSessionNotFound) until they expire
// rather than being merged on a guess.
func (s *PlaybackSessionStore) ResolveClientPlaySessionID(token, clientPlayID string) (*PlaybackSession, error) {
	if session, ok := s.findByClientPlaySessionID(token, clientPlayID, "", "", false); ok {
		return session, nil
	}
	return nil, ErrSessionNotFound
}

func (d *DurableCompatPlaybackStore) ResolveClientPlaySessionID(token, clientPlayID string) (*PlaybackSession, error) {
	if d.pool == nil {
		return d.mem.ResolveClientPlaySessionID(token, clientPlayID)
	}
	if token == "" || clientPlayID == "" {
		return nil, ErrSessionNotFound
	}
	cached, cachedOK := d.mem.findByClientPlaySessionID(token, clientPlayID, "", "", false)
	if cachedOK && !d.shouldRevalidateToken(token) {
		return cached, nil
	}
	if err := d.loadByCompatToken(token); err != nil {
		if cachedOK {
			return cached, nil
		}
		return nil, err
	}
	if session, ok := d.mem.findByClientPlaySessionID(token, clientPlayID, "", "", false); ok {
		return session, nil
	}
	return nil, ErrSessionNotFound
}
