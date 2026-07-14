package domain

import (
	"errors"
	"testing"
)

func TestIDConstructorsRejectEmptyValues(t *testing.T) {
	constructors := []struct {
		name string
		new  func(string) (string, error)
	}{
		{name: "repository", new: func(value string) (string, error) { id, err := NewRepositoryID(value); return string(id), err }},
		{name: "worktree", new: func(value string) (string, error) { id, err := NewWorktreeID(value); return string(id), err }},
		{name: "review session", new: func(value string) (string, error) { id, err := NewReviewSessionID(value); return string(id), err }},
		{name: "session lease", new: func(value string) (string, error) { id, err := NewSessionLeaseID(value); return string(id), err }},
		{name: "review thread", new: func(value string) (string, error) { id, err := NewReviewThreadID(value); return string(id), err }},
		{name: "message", new: func(value string) (string, error) { id, err := NewMessageID(value); return string(id), err }},
		{name: "proposal", new: func(value string) (string, error) { id, err := NewProposalID(value); return string(id), err }},
		{name: "workspace", new: func(value string) (string, error) { id, err := NewWorkspaceID(value); return string(id), err }},
		{name: "capture", new: func(value string) (string, error) { id, err := NewCaptureID(value); return string(id), err }},
		{name: "review snapshot", new: func(value string) (string, error) { id, err := NewReviewSnapshotID(value); return string(id), err }},
		{name: "provider conversation", new: func(value string) (string, error) {
			id, err := NewProviderConversationID(value)
			return string(id), err
		}},
		{name: "provider turn", new: func(value string) (string, error) { id, err := NewProviderTurnID(value); return string(id), err }},
		{name: "operation", new: func(value string) (string, error) { id, err := NewOperationID(value); return string(id), err }},
	}

	for _, test := range constructors {
		t.Run(test.name, func(t *testing.T) {
			value, err := test.new("")
			if !errors.Is(err, ErrEmptyID) {
				t.Fatalf("error = %v, want ErrEmptyID", err)
			}
			if value != "" {
				t.Fatalf("value = %q, want empty value", value)
			}
		})
	}
}

func TestIDConstructorsPreserveOpaqueValues(t *testing.T) {
	const raw = "opaque/value with spaces"

	id, err := NewRepositoryID(raw)
	if err != nil {
		t.Fatalf("construct repository ID: %v", err)
	}
	if string(id) != raw {
		t.Fatalf("ID = %q, want %q", id, raw)
	}
}
