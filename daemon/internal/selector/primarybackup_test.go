package selector

import "testing"

// strPtr is a tiny helper for building *string literals in test tables.
func strPtr(s string) *string { return &s }

func TestPrimaryBackup(t *testing.T) {
	t.Parallel()

	g := Group{Name: "home", Strategy: "primary-backup"}

	tests := []struct {
		name    string
		members []MemberHealth
		want    *string
	}{
		{
			name:    "empty member list yields nil Active",
			members: []MemberHealth{},
			want:    nil,
		},
		{
			name: "single healthy member is picked",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
			},
			want: strPtr("primary"),
		},
		{
			name: "single unhealthy member yields nil Active",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: false},
			},
			want: nil,
		},
		{
			name: "lowest priority among healthy wins",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "backup", Priority: 2}, Healthy: true},
			},
			want: strPtr("primary"),
		},
		{
			name: "fails over to next priority when primary unhealthy",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: false},
				{Member: Member{Wan: "backup", Priority: 2}, Healthy: true},
			},
			want: strPtr("backup"),
		},
		{
			name: "all unhealthy yields nil Active",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: false},
				{Member: Member{Wan: "backup", Priority: 2}, Healthy: false},
			},
			want: nil,
		},
		{
			name: "priority order respected regardless of input order",
			members: []MemberHealth{
				{Member: Member{Wan: "backup", Priority: 5}, Healthy: true},
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "middle", Priority: 3}, Healthy: true},
			},
			want: strPtr("primary"),
		},
		{
			name: "equal priorities broken by wan-name lex order",
			members: []MemberHealth{
				{Member: Member{Wan: "zzz", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "aaa", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "mmm", Priority: 1}, Healthy: true},
			},
			want: strPtr("aaa"),
		},
		{
			name: "weight is ignored by primary-backup",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1, Weight: 1}, Healthy: true},
				{Member: Member{Wan: "backup", Priority: 2, Weight: 1000}, Healthy: true},
			},
			want: strPtr("primary"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := primaryBackup(g, tc.members)
			if got.Group != g.Name {
				t.Errorf("Group = %q, want %q", got.Group, g.Name)
			}
			switch {
			case tc.want == nil && got.Active != nil:
				t.Errorf("Active = %q, want nil", *got.Active)
			case tc.want != nil && got.Active == nil:
				t.Errorf("Active = nil, want %q", *tc.want)
			case tc.want != nil && got.Active != nil && *got.Active != *tc.want:
				t.Errorf("Active = %q, want %q", *got.Active, *tc.want)
			}
		})
	}
}

func TestPrimaryBackupIsDeterministic(t *testing.T) {
	t.Parallel()
	g := Group{Name: "home", Strategy: "primary-backup"}
	members := []MemberHealth{
		{Member: Member{Wan: "a", Priority: 1}, Healthy: true},
		{Member: Member{Wan: "b", Priority: 1}, Healthy: true},
		{Member: Member{Wan: "c", Priority: 1}, Healthy: true},
	}
	first := primaryBackup(g, members)
	for i := 0; i < 100; i++ {
		got := primaryBackup(g, members)
		if got.Active == nil || first.Active == nil {
			t.Fatalf("iteration %d: got nil Active", i)
		}
		if *got.Active != *first.Active {
			t.Errorf("iteration %d: Active = %q, first call returned %q", i, *got.Active, *first.Active)
		}
	}
}

func TestPrimaryBackupDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	original := []MemberHealth{
		{Member: Member{Wan: "z", Priority: 1}, Healthy: true},
		{Member: Member{Wan: "a", Priority: 1}, Healthy: true},
	}
	copy_ := make([]MemberHealth, len(original))
	copy(copy_, original)

	_ = primaryBackup(Group{}, copy_)

	for i := range original {
		if copy_[i] != original[i] {
			t.Errorf("input mutated at index %d: %+v vs %+v", i, copy_[i], original[i])
		}
	}
}
