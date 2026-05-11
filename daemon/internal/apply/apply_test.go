package apply

import "testing"

func TestFamilyString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		f    Family
		want string
	}{
		{FamilyV4, "v4"},
		{FamilyV6, "v6"},
		{Family(99), "Family(99)"},
	}
	for _, tc := range cases {
		if got := tc.f.String(); got != tc.want {
			t.Errorf("Family(%d).String() = %q, want %q", int(tc.f), got, tc.want)
		}
	}
}

func TestFamilyFromString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   Family
		wantOk bool
	}{
		{"v4", FamilyV4, true},
		{"v6", FamilyV6, true},
		{"", 0, false},
		{"v7", 0, false},
		{"V4", 0, false}, // case-sensitive — matches PLAN §5.5 wire format
	}
	for _, tc := range cases {
		got, ok := FamilyFromString(tc.in)
		if got != tc.want || ok != tc.wantOk {
			t.Errorf("FamilyFromString(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOk)
		}
	}
}
