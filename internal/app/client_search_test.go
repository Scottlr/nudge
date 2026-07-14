package app

import (
	"context"
	"testing"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

func TestClientRoutesSearchTreeQueryToConsumerPort(t *testing.T) {
	searcher := clientSearchStub{}
	client, err := NewClient(ClientOptions{TreeSearcher: searcher})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	result, err := client.Query(context.Background(), SearchTreeQuery{Snapshot: repository.SnapshotRef{Kind: repository.SnapshotEmpty}, Query: "src", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.SearchTree == nil || result.SearchTree.Snapshot.Kind != repository.SnapshotEmpty {
		t.Fatalf("search query result = %#v", result)
	}
}

func TestClientRoutesLargeContentWindowQueryToConsumerPort(t *testing.T) {
	client, err := NewClient(ClientOptions{LargeContent: clientLargeContentStub{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	result, err := client.Query(context.Background(), LargeContentWindowQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentWindow == nil || !result.ContentWindow.Complete {
		t.Fatalf("large-content query result = %#v", result)
	}
}

type clientSearchStub struct{}

func (clientSearchStub) SearchTree(_ context.Context, query SearchTreeQuery) (SearchTreePage, error) {
	return SearchTreePage{Snapshot: query.Snapshot, Complete: true}, nil
}

var _ TreeSearcher = clientSearchStub{}

type clientLargeContentStub struct{}

func (clientLargeContentStub) ReadRange(context.Context, LargeContentRangeRequest) (ContentRange, error) {
	return ContentRange{}, nil
}

func (clientLargeContentStub) ReadLines(context.Context, LargeContentWindowRequest) (ContentWindow, error) {
	return ContentWindow{Complete: true, NextLine: 1}, nil
}

var _ LargeContentQueryPort = clientLargeContentStub{}
