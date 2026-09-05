package delegation

import (
	"testing"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScopeSet_NewScopeSet(t *testing.T) {
	ss := NewScopeSet([]string{"write:data", "read:data", "admin:all"})
	// Must be sorted and deduplicated
	assert.Equal(t, ScopeSet{"admin:all", "read:data", "write:data"}, ss)
}

func TestScopeSet_NewScopeSet_Deduplicates(t *testing.T) {
	ss := NewScopeSet([]string{"read:data", "read:data", "write:data"})
	assert.Equal(t, ScopeSet{"read:data", "write:data"}, ss)
}

func TestScopeSet_NewScopeSet_Empty(t *testing.T) {
	ss := NewScopeSet(nil)
	assert.Nil(t, ss)
}

func TestScopeSet_Intersection(t *testing.T) {
	tests := []struct {
		name    string
		a       ScopeSet
		b       ScopeSet
		want    ScopeSet
		wantErr error
	}{
		{
			name: "normal intersection",
			a:    NewScopeSet([]string{"read:data", "write:data"}),
			b:    NewScopeSet([]string{"read:data", "delete:data"}),
			want: NewScopeSet([]string{"read:data"}),
		},
		{
			name: "identical sets",
			a:    NewScopeSet([]string{"read:data", "write:data"}),
			b:    NewScopeSet([]string{"read:data", "write:data"}),
			want: NewScopeSet([]string{"read:data", "write:data"}),
		},
		{
			name:    "disjoint sets",
			a:       NewScopeSet([]string{"read:data"}),
			b:       NewScopeSet([]string{"write:data"}),
			wantErr: ErrScopeIntersectionEmpty,
		},
		{
			name:    "empty parent scope",
			a:       NewScopeSet(nil),
			b:       NewScopeSet([]string{"read:data"}),
			wantErr: ErrScopeIntersectionEmpty,
		},
		{
			name:    "empty granted scope",
			a:       NewScopeSet([]string{"read:data"}),
			b:       NewScopeSet(nil),
			wantErr: ErrScopeIntersectionEmpty,
		},
		{
			name:    "both empty",
			a:       NewScopeSet(nil),
			b:       NewScopeSet(nil),
			wantErr: ErrScopeIntersectionEmpty,
		},
		{
			name: "subset intersection",
			a:    NewScopeSet([]string{"read:data", "write:data", "delete:data"}),
			b:    NewScopeSet([]string{"read:data"}),
			want: NewScopeSet([]string{"read:data"}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.a.Intersection(tt.b)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.want == nil {
				assert.Nil(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestScopeSet_Intersection_Error(t *testing.T) {
	a := NewScopeSet([]string{"read:data"})
	b := NewScopeSet([]string{"write:data"})
	_, err := a.Intersection(b)
	require.ErrorIs(t, err, ErrScopeIntersectionEmpty)
}

func TestGrantEnvelope_Fields(t *testing.T) {
	now := time.Now()
	g := &Grant{
		GrantID:         domain.NewUUID(),
		ParentGrantID:   nil,
		ChainID:         domain.NewUUID(),
		TenantID:        domain.NewUUID(),
		DelegateIdentity: "agent-42",
		GrantedScope:    NewScopeSet([]string{"read:data"}),
		RootIntent:      "read:customer-records",
		HITLClassification: "required",
		Depth:           0,
		ExpiresAt:       now.Add(time.Hour),
		BudgetRemaining: 1000,
		Status:          GrantStatusIssued,
	}

	assert.Equal(t, "agent-42", g.DelegateIdentity)
	assert.Equal(t, "read:customer-records", g.RootIntent)
	assert.Equal(t, "required", g.HITLClassification)
	assert.Equal(t, 0, g.Depth)
	assert.Equal(t, 1000, g.BudgetRemaining)
	assert.Equal(t, GrantStatusIssued, g.Status)
	assert.Nil(t, g.ParentGrantID)
	assert.False(t, g.ExpiresAt.IsZero())
}

func TestGrant_HasParent(t *testing.T) {
	g := &Grant{ParentGrantID: nil}
	assert.False(t, g.HasParent())

	parentID := domain.NewUUID()
	g.ParentGrantID = &parentID
	assert.True(t, g.HasParent())
}

func TestGrant_IsExpired(t *testing.T) {
	g := &Grant{
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	assert.True(t, g.IsExpired())

	g.ExpiresAt = time.Now().Add(time.Hour)
	assert.False(t, g.IsExpired())
}

func TestGrant_EffectiveScope(t *testing.T) {
	g := &Grant{
		GrantedScope: NewScopeSet([]string{"read:data", "write:data"}),
	}
	es := g.EffectiveScope()
	assert.Equal(t, ScopeSet{"read:data", "write:data"}, es)
}
