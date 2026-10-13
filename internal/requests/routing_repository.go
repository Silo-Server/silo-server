package requests

import (
	"context"
	"encoding/json"
	"fmt"
)

const routeColumns = `id, media_type, position, name, enabled, is_fallback, conditions,
	coalesce(hd_integration_id, ''), hd_overrides, coalesce(uhd_integration_id, ''), uhd_overrides,
	skip_uhd, revision`

func (r *Repository) ListRoutes(ctx context.Context) ([]Route, error) {
	return listRoutes(ctx, r.pool)
}

func listRoutes(ctx context.Context, exec requestExecutor) ([]Route, error) {
	rows, err := exec.Query(ctx, `SELECT `+routeColumns+` FROM request_routes ORDER BY media_type, is_fallback, position, id`)
	if err != nil {
		return nil, fmt.Errorf("list request routes: %w", err)
	}
	defer rows.Close()
	var out []Route
	for rows.Next() {
		route, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, rows.Err()
}

func scanRoute(row requestScanner) (Route, error) {
	var route Route
	var conditions, hdOverrides, uhdOverrides []byte
	if err := row.Scan(&route.ID, &route.MediaType, &route.Position, &route.Name, &route.Enabled, &route.IsFallback,
		&conditions, &route.HD.IntegrationID, &hdOverrides, &route.UHD.IntegrationID, &uhdOverrides,
		&route.SkipUHD, &route.Revision); err != nil {
		return Route{}, fmt.Errorf("scan request route: %w", err)
	}
	if err := json.Unmarshal(conditions, &route.Conditions); err != nil {
		return Route{}, fmt.Errorf("decode route %s conditions: %w", route.ID, err)
	}
	if err := json.Unmarshal(hdOverrides, &route.HD.Overrides); err != nil {
		return Route{}, fmt.Errorf("decode route %s HD overrides: %w", route.ID, err)
	}
	if err := json.Unmarshal(uhdOverrides, &route.UHD.Overrides); err != nil {
		return Route{}, fmt.Errorf("decode route %s 4K overrides: %w", route.ID, err)
	}
	return route, nil
}

func (r *Repository) SetRoutingFacts(ctx context.Context, id string, facts RoutingFacts) (*Request, error) {
	raw, err := encodeRoutingFacts(facts)
	if err != nil {
		return nil, err
	}
	req, err := scanRequest(r.pool.QueryRow(ctx, `
		UPDATE media_requests SET routing_facts = $2, is_anime = $3
		WHERE id = $1
		RETURNING `+requestColumns(), id, raw, facts.Anime))
	if err != nil {
		return nil, fmt.Errorf("set request routing facts: %w", err)
	}
	return req, nil
}
