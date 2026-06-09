package jellycompat

import "testing"

func TestMediaSourceIDsEqual(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{
			name: "identical dashed",
			a:    "03000000-0000-0000-0000-00000019e8c2",
			b:    "03000000-0000-0000-0000-00000019e8c2",
			want: true,
		},
		{
			name: "dashed vs compact (Wholphin)",
			a:    "03000000-0000-0000-0000-00000019e8c2",
			b:    "0300000000000000000000000019e8c2",
			want: true,
		},
		{
			name: "compact vs dashed",
			a:    "0300000000000000000000000019e8c2",
			b:    "03000000-0000-0000-0000-00000019e8c2",
			want: true,
		},
		{
			name: "case insensitive hex",
			a:    "03000000-0000-0000-0000-00000019E8C2",
			b:    "0300000000000000000000000019e8c2",
			want: true,
		},
		{
			name: "different uuids",
			a:    "03000000-0000-0000-0000-00000019e8c2",
			b:    "0300000000000000000000000019e8c3",
			want: false,
		},
		{
			name: "non-uuid falls back to exact match",
			a:    "abc",
			b:    "abc",
			want: true,
		},
		{
			name: "non-uuid mismatch",
			a:    "abc",
			b:    "def",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaSourceIDsEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("mediaSourceIDsEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
