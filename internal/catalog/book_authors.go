package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// bookAuthorsSQL lists up to $3 author names per content ID in $1, in credit
// order. Like audiobookPeopleFromCrew on the detail page it keeps the author
// credits whose name is not blank by Go's strings.TrimSpace.
var bookAuthorsSQL = `
	SELECT target.content_id, author.name
	FROM unnest($1::text[]) WITH ORDINALITY AS target(content_id, ord)
	CROSS JOIN LATERAL (
		SELECT p.name, ip.sort_order, ip.person_id
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = target.content_id
		  AND ip.kind = $2
		  AND BTRIM(p.name, ` + SQLTrimSpaceChars + `) <> ''
		ORDER BY ip.sort_order, ip.person_id
		LIMIT $3
	) author
	ORDER BY target.ord, author.sort_order, author.person_id`

// ListBookAuthors returns at most limit author names for each of contentIDs,
// in credit order: the first names a book's detail page lists under its
// authors. One query covers every ID, so callers pass one page of cards;
// repeated and empty IDs are dropped first. An ID with no author credit is
// absent.
func ListBookAuthors(ctx context.Context, pool *pgxpool.Pool, contentIDs []string, limit int) (map[string][]string, error) {
	ids := make([]string, 0, len(contentIDs))
	seen := make(map[string]struct{}, len(contentIDs))
	for _, id := range contentIDs {
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	authors := make(map[string][]string, len(ids))
	if pool == nil || len(ids) == 0 || limit <= 0 {
		return authors, nil
	}
	rows, err := pool.Query(ctx, bookAuthorsSQL, ids, int(models.PersonKindAuthor), limit)
	if err != nil {
		return nil, fmt.Errorf("listing book authors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var contentID, name string
		if err := rows.Scan(&contentID, &name); err != nil {
			return nil, fmt.Errorf("scanning book author: %w", err)
		}
		authors[contentID] = append(authors[contentID], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating book authors: %w", err)
	}
	return authors, nil
}
