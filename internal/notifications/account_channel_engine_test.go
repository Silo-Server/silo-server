package notifications

// fakeDeliveryWindow serves listSince-style paged reads over a fixed,
// (created_at, id)-ordered dataset, mirroring the exclusive-cursor semantics
// of the real delivery queries.
type fakeDeliveryWindow struct {
	rows    []DeliveryRow
	fetches int
}

func (f *fakeDeliveryWindow) fetch(since Cursor, limit int) ([]DeliveryRow, error) {
	f.fetches++
	out := make([]DeliveryRow, 0, limit)
	for _, row := range f.rows {
		if !cursorLess(since, Cursor{CreatedAt: row.CreatedAt, ID: row.ID}) {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
