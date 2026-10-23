package catalog

import "testing"

func TestSeasonBadge(t *testing.T) {
	cases := []struct {
		name string
		in   SeasonAvailability
		want Availability
		ok   bool
	}{
		{name: "all episodes present", in: SeasonAvailability{Aired: 8, Total: 10, Have: 10}, want: Availability{Have: 10, Total: 10}, ok: true},
		{name: "some missing", in: SeasonAvailability{Aired: 10, Total: 10, Have: 8}, want: Availability{Have: 8, Total: 10}, ok: true},
		{name: "still airing", in: SeasonAvailability{Aired: 8, Upcoming: 2, Total: 10, Have: 8}, want: Availability{Have: 8, Total: 10}, ok: true},
		{name: "early file before premiere", in: SeasonAvailability{Upcoming: 2, Total: 2, Have: 1}, want: Availability{Have: 1, Total: 2}, ok: true},
		{name: "nothing aired and nothing owned", in: SeasonAvailability{Upcoming: 10, Total: 10}},
		{name: "no episodes", in: SeasonAvailability{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SeasonBadge(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("SeasonBadge(%+v) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestSeriesBadge(t *testing.T) {
	seasons := map[int]SeasonAvailability{
		0: {Aired: 2, Total: 2, Have: 2},               // specials stay out
		1: {Aired: 10, Total: 10, Have: 10},            // complete
		2: {Aired: 8, Upcoming: 2, Total: 10, Have: 8}, // released, not complete
		3: {Upcoming: 8, Total: 8},                     // not released
		4: {Aired: 6, Total: 6, Have: 6, HaveAired: 6}, // complete
	}
	got, ok := SeriesBadge(seasons)
	want := Availability{Have: 2, Total: 3}
	if !ok || got != want {
		t.Fatalf("SeriesBadge = %+v, %v; want %+v, true", got, ok, want)
	}

	if _, ok := SeriesBadge(map[int]SeasonAvailability{1: {Upcoming: 4, Total: 4}}); ok {
		t.Fatal("a series with no released season should not badge")
	}
	all, ok := SeriesBadge(map[int]SeasonAvailability{1: {Aired: 4, Total: 4, Have: 4}})
	if !ok || all != (Availability{Have: 1, Total: 1}) {
		t.Fatalf("complete series = %+v, %v", all, ok)
	}
}
