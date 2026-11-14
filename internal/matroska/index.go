package matroska

// SeekEntry is one Seek of a SeekHead: an element's ID and its position
// relative to the start of the segment's data.
type SeekEntry struct {
	ID       uint64
	Position int64
}

// maxPosition bounds positions read from the file, so offsets computed from
// them can't overflow.
const maxPosition = 1 << 62

// WalkSeekEntries calls fn with each of a SeekHead's entries in file order,
// keeping no list a crafted SeekHead of tiny entries could expand. An entry
// missing its ID or position, or whose position doesn't fit, is left out.
func WalkSeekEntries(seekHead []byte, fn func(SeekEntry)) {
	Children(seekHead, func(id uint64, seek []byte) {
		if id != IDSeek {
			return
		}
		var (
			seekID  uint64
			seekPos int64 = -1
		)
		Children(seek, func(id uint64, v []byte) {
			switch id {
			case IDSeekID:
				seekID = Uint(v)
			case IDSeekPosition:
				if p := Uint(v); p < maxPosition {
					seekPos = int64(p)
				}
			}
		})
		if seekID != 0 && seekPos >= 0 {
			fn(SeekEntry{ID: seekID, Position: seekPos})
		}
	})
}

// SeekPosition returns the position, relative to the segment's data, of the
// first SeekHead entry for target.
func SeekPosition(seekHead []byte, target uint64) (int64, bool) {
	var (
		pos   int64
		found bool
	)
	WalkSeekEntries(seekHead, func(e SeekEntry) {
		if !found && e.ID == target {
			pos, found = e.Position, true
		}
	})
	return pos, found
}

// CueTrackPosition is one CueTrackPositions entry of a CuePoint: where one
// track's block at the point's time is. Absent optional children are -1
// (positions and duration) or zero.
type CueTrackPosition struct {
	Track uint64
	// ClusterPosition is the cluster's position relative to the start of
	// the segment's data.
	ClusterPosition int64
	// RelativePosition is the block's position relative to the start of
	// the cluster's data.
	RelativePosition int64
	// Duration is the block's duration in ticks.
	Duration    int64
	BlockNumber uint64
	CodecState  uint64
	// HasReference reports a CueReference: the block needs other frames.
	HasReference bool
}

// WalkCuePoints calls fn for each CueTrackPositions of each CuePoint in a
// Cues element's data, with the point's CueTime; hasTime is false when the
// point has none. It reports false when malformed data stopped the walk of
// the Cues, a CuePoint or a CueTrackPositions early. Entries read before the
// malformed data are still passed to fn.
func WalkCuePoints(cues []byte, fn func(time uint64, hasTime bool, pos CueTrackPosition)) bool {
	complete := true
	complete = ChildrenComplete(cues, func(id uint64, point []byte) {
		if id != IDCuePoint {
			return
		}
		var (
			t       uint64
			hasTime bool
		)
		// CueTime may follow the positions, so find it first.
		if !ChildrenComplete(point, func(id uint64, v []byte) {
			if id == IDCueTime {
				t, hasTime = Uint(v), true
			}
		}) {
			complete = false
		}
		Children(point, func(id uint64, v []byte) {
			if id != IDCueTrackPositions {
				return
			}
			pos := CueTrackPosition{ClusterPosition: -1, RelativePosition: -1, Duration: -1}
			if !ChildrenComplete(v, func(id uint64, v []byte) {
				switch id {
				case IDCueTrack:
					pos.Track = Uint(v)
				case IDCueClusterPosition:
					if p := Uint(v); p < maxPosition {
						pos.ClusterPosition = int64(p)
					}
				case IDCueRelativePosition:
					if p := Uint(v); p < maxPosition {
						pos.RelativePosition = int64(p)
					}
				case IDCueDuration:
					if d := Uint(v); d < maxPosition {
						pos.Duration = int64(d)
					}
				case IDCueBlockNumber:
					pos.BlockNumber = Uint(v)
				case IDCueCodecState:
					pos.CodecState = Uint(v)
				case IDCueReference:
					pos.HasReference = true
				}
			}) {
				complete = false
			}
			fn(t, hasTime, pos)
		})
	}) && complete
	return complete
}
