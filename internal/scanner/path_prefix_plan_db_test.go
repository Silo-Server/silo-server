package scanner

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestFolderPathPrefixQueryUsesPathRangeUnderGenericPlanDB pins the plan of
// subtree file reads. pgx prepares its statements, and PostgreSQL can switch a
// prepared statement to a generic plan after five executions. A generic plan
// cannot turn a parameterized LIKE into index bounds, so it would read every
// file of the folder and filter them; the range bounds keep the subtree read
// on the (media_folder_id, file_path text_pattern_ops) index.
func TestFolderPathPrefixQueryUsesPathRangeUnderGenericPlanDB(t *testing.T) {
	ctx := t.Context()
	pool := newDeadRootTestPool(t)
	folderID := seedDeadRootTestFolder(t, pool, "series", "Path prefix generic plan")
	base := fmt.Sprintf("/prefix-plan-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, file_size)
		SELECT $1, $2 || '/show-' || (g / 50) || '/episode-' || g || '.mkv', 1024
		FROM generate_series(1, 10000) g
	`, folderID, base); err != nil {
		t.Fatalf("seed folder files: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE media_files`); err != nil {
		t.Fatalf("analyze media_files: %v", err)
	}

	query, _ := folderPathPrefixQuery(fileColumns, folderID, base+"/show-7")
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// GENERIC_PLAN plans the statement with its $n placeholders unbound, which
	// only the simple query protocol accepts.
	results, err := conn.Conn().PgConn().Exec(ctx, `EXPLAIN (GENERIC_PLAN, FORMAT JSON) `+query).ReadAll()
	if err != nil || len(results) != 1 || len(results[0].Rows) != 1 {
		t.Fatalf("explain generic plan: %v", err)
	}
	raw := results[0].Rows[0][0]
	var explained []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &explained); err != nil || len(explained) != 1 {
		t.Fatalf("decode plan %s: %v", raw, err)
	}
	var rangeIndexed bool
	var scans []string
	explained[0].Plan.walk(func(node planNode) {
		if node.RelationName == "media_files" && node.NodeType == "Seq Scan" {
			t.Errorf("generic plan reads media_files sequentially: %s", raw)
		}
		if node.IndexName == "" {
			return
		}
		scans = append(scans, node.NodeType+" "+node.IndexName+" "+node.IndexCond)
		if strings.Contains(node.IndexCond, "file_path ~>=~ $3") && strings.Contains(node.IndexCond, "file_path ~<~ $4") {
			rangeIndexed = true
		}
	})
	if !rangeIndexed {
		t.Fatalf("generic plan does not bound file_path in an index condition: %q", scans)
	}
}

type planNode struct {
	NodeType     string     `json:"Node Type"`
	RelationName string     `json:"Relation Name"`
	IndexName    string     `json:"Index Name"`
	IndexCond    string     `json:"Index Cond"`
	Plans        []planNode `json:"Plans"`
}

func (n planNode) walk(visit func(planNode)) {
	visit(n)
	for _, child := range n.Plans {
		child.walk(visit)
	}
}
