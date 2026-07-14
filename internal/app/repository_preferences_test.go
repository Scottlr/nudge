package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Scottlr/nudge/internal/domain"
)

type preferenceTestStore struct {
	preference *BaseBranchPreference
	loadCalls  int
	saveCalls  int
	clearCalls int
}

func (s *preferenceTestStore) LoadBaseBranchPreference(context.Context, domain.RepositoryID) (*BaseBranchPreference, error) {
	s.loadCalls++
	if s.preference == nil {
		return nil, ErrReviewStoreNotFound
	}
	value := *s.preference
	return &value, nil
}

func (s *preferenceTestStore) SaveBaseBranchPreference(_ context.Context, preference BaseBranchPreference, expectedRevision uint64) error {
	s.saveCalls++
	if s.preference != nil && s.preference.Revision != expectedRevision {
		return ErrPreferenceRevisionConflict
	}
	s.preference = &preference
	return nil
}

func (s *preferenceTestStore) ClearBaseBranchPreference(_ context.Context, _ domain.RepositoryID, expectedRevision uint64) error {
	s.clearCalls++
	if s.preference != nil && s.preference.Revision != expectedRevision {
		return ErrPreferenceRevisionConflict
	}
	s.preference = nil
	return nil
}

func TestSelectBaseBranchUsesExactPrecedence(t *testing.T) {
	repositoryID := domain.RepositoryID("repo-1")
	validPreference := &BaseBranchPreference{RepositoryID: repositoryID, Expression: "refs/heads/saved", Revision: 3, UpdatedAt: time.Unix(10, 0).UTC()}
	cases := []struct {
		name       string
		explicit   string
		session    string
		preference *BaseBranchPreference
		noPersist  bool
		want       BaseBranchSelection
		loads      int
		discovers  int
	}{
		{name: "explicit", explicit: "refs/heads/flag", session: "refs/heads/session", preference: validPreference, want: BaseBranchSelection{Expression: "refs/heads/flag", Source: BaseFromExplicitFlag}},
		{name: "session", session: "refs/heads/session", preference: validPreference, want: BaseBranchSelection{Expression: "refs/heads/session", Source: BaseFromSessionChoice}},
		{name: "saved", preference: validPreference, want: BaseBranchSelection{Expression: "refs/heads/saved", Source: BaseFromPreference}, loads: 1},
		{name: "no-persist-discovery", preference: validPreference, noPersist: true, want: BaseBranchSelection{Expression: "refs/heads/discovered", Source: BaseFromDiscovery}, discovers: 1},
		{name: "missing-discovery", want: BaseBranchSelection{Expression: "refs/heads/discovered", Source: BaseFromDiscovery}, loads: 1, discovers: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := &preferenceTestStore{preference: test.preference}
			discovers := 0
			selection, err := SelectBaseBranch(context.Background(), BaseBranchSelectionRequest{
				RepositoryID:       repositoryID,
				ExplicitExpression: test.explicit,
				SessionExpression:  test.session,
				Persistence: func() PersistenceMode {
					if test.noPersist {
						return PersistenceNoPersist
					}
					return PersistenceDurable
				}(),
				Store: store,
				Discover: func(context.Context) (string, error) {
					discovers++
					return "refs/heads/discovered", nil
				},
			})
			if err != nil {
				t.Fatalf("select base: %v", err)
			}
			if selection != test.want {
				t.Fatalf("selection = %#v, want %#v", selection, test.want)
			}
			if store.loadCalls != test.loads || discovers != test.discovers {
				t.Fatalf("calls = load %d/discover %d, want %d/%d", store.loadCalls, discovers, test.loads, test.discovers)
			}
		})
	}
}

func TestSelectBaseBranchDoesNotFallThroughInvalidSavedPreference(t *testing.T) {
	store := &preferenceTestStore{}
	store.preference = &BaseBranchPreference{RepositoryID: domain.RepositoryID("repo-1"), Expression: "-unsafe", Revision: 1, UpdatedAt: time.Unix(10, 0).UTC()}
	discoveries := 0
	_, err := SelectBaseBranch(context.Background(), BaseBranchSelectionRequest{
		RepositoryID: domain.RepositoryID("repo-1"),
		Store:        store,
		Discover: func(context.Context) (string, error) {
			discoveries++
			return "refs/heads/main", nil
		},
	})
	if !errors.Is(err, ErrSavedBaseUnavailable) || discoveries != 0 {
		t.Fatalf("error = %v, discoveries = %d, want saved unavailable and zero discovery", err, discoveries)
	}
}

func TestBaseBranchExpressionValidation(t *testing.T) {
	cases := []string{"", "-main", "main\x00", "main\n", string(make([]byte, MaxBaseBranchExpressionBytes+1))}
	for _, value := range cases {
		if err := ValidateBaseBranchExpression(value); !errors.Is(err, ErrInvalidBaseBranchPreference) {
			t.Fatalf("expression %q error = %v, want invalid preference", value, err)
		}
	}
	if err := ValidateBaseBranchExpression("refs/heads/feature ü"); err != nil {
		t.Fatalf("valid expression rejected: %v", err)
	}
}

func TestRepositoryPreferenceActionsDisableNoPersist(t *testing.T) {
	actions := RepositoryPreferenceActions{Store: &preferenceTestStore{}, Persistence: PersistenceNoPersist}
	if err := actions.Save(context.Background(), SaveBaseBranchPreference{RepositoryID: domain.RepositoryID("repo-1"), Expression: "main"}); !errors.Is(err, ErrPreferencePersistenceDisabled) {
		t.Fatalf("save error = %v, want persistence disabled", err)
	}
	if err := actions.Clear(context.Background(), ClearBaseBranchPreference{RepositoryID: domain.RepositoryID("repo-1")}); !errors.Is(err, ErrPreferencePersistenceDisabled) {
		t.Fatalf("clear error = %v, want persistence disabled", err)
	}
}
