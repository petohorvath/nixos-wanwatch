package selector

import "testing"

func TestPrimaryBackup(t *testing.T) {
	t.Parallel()

	g := Group{Name: "home", Strategy: "primary-backup"}

	tests := []struct {
		name    string
		members []MemberHealth
		want    Active
	}{
		{
			name:    "empty member list yields absent Active",
			members: []MemberHealth{},
			want:    NoActive,
		},
		{
			name: "single healthy member is picked",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
			},
			want: Active{Wan: "primary", Has: true},
		},
		{
			name: "single unhealthy member yields absent Active",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: false},
			},
			want: NoActive,
		},
		{
			name: "lowest priority among healthy wins",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "backup", Priority: 2}, Healthy: true},
			},
			want: Active{Wan: "primary", Has: true},
		},
		{
			name: "fails over to next priority when primary unhealthy",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: false},
				{Member: Member{Wan: "backup", Priority: 2}, Healthy: true},
			},
			want: Active{Wan: "backup", Has: true},
		},
		{
			name: "all unhealthy yields absent Active",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: false},
				{Member: Member{Wan: "backup", Priority: 2}, Healthy: false},
			},
			want: NoActive,
		},
		{
			name: "priority order respected regardless of input order",
			members: []MemberHealth{
				{Member: Member{Wan: "backup", Priority: 5}, Healthy: true},
				{Member: Member{Wan: "primary", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "middle", Priority: 3}, Healthy: true},
			},
			want: Active{Wan: "primary", Has: true},
		},
		{
			name: "equal priorities broken by wan-name lex order",
			members: []MemberHealth{
				{Member: Member{Wan: "zzz", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "aaa", Priority: 1}, Healthy: true},
				{Member: Member{Wan: "mmm", Priority: 1}, Healthy: true},
			},
			want: Active{Wan: "aaa", Has: true},
		},
		{
			name: "weight is ignored by primary-backup",
			members: []MemberHealth{
				{Member: Member{Wan: "primary", Priority: 1, Weight: 1}, Healthy: true},
				{Member: Member{Wan: "backup", Priority: 2, Weight: 1000}, Healthy: true},
			},
			want: Active{Wan: "primary", Has: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := primaryBackup(g, tc.members)
			if got.Group != g.Name {
				t.Errorf("Group = %q, want %q", got.Group, g.Name)
			}
			if got.Active != tc.want {
				t.Errorf("Active = %+v, want %+v", got.Active, tc.want)
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
		if !got.Active.Has || !first.Active.Has {
			t.Fatalf("iteration %d: got absent Active", i)
		}
		if got.Active != first.Active {
			t.Errorf("iteration %d: Active = %+v, first call returned %+v", i, got.Active, first.Active)
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
