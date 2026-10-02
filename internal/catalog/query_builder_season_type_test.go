package catalog

import (
	"strings"
	"testing"
)

func seriesTypeRule(op string) QueryDefinition {
	return QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "type", Op: op, Value: "series"}}}}}
}

func TestSeriesTypeRuleIncludesSeasonsWhenEnabled(t *testing.T) {
	sql, args, err := NewQueryBuilder("mi").WithSeasonsMatchSeriesType(true).Build(seriesTypeRule("is"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "mi.type IN ('series', 'season')") || len(args) != 0 {
		t.Fatalf("is series: %s %#v", sql, args)
	}
	sql, _, err = NewQueryBuilder("mi").WithSeasonsMatchSeriesType(true).Build(seriesTypeRule("is_not"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "mi.type NOT IN ('series', 'season')") {
		t.Fatalf("is_not series: %s", sql)
	}
}

func TestSeriesTypeRuleUnchangedByDefault(t *testing.T) {
	sql, args, err := NewQueryBuilder("mi").Build(seriesTypeRule("is"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "season") || len(args) != 1 || args[0] != "series" {
		t.Fatalf("default series rule changed: %s %#v", sql, args)
	}
}
