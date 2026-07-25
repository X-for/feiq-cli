package history

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreHistoryAndSearchUsers(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "nested", "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	entries := []Entry{
		{Time: base, Direction: "out", PeerIP: "192.168.1.2", Kind: "msg", Content: "hello"},
		{Time: base.Add(time.Minute), Direction: "in", PeerIP: "192.168.1.3", PeerName: "Alice", Kind: "msg", Content: "hi"},
		{Time: base.Add(2 * time.Minute), Direction: "in", PeerIP: "192.168.1.2", PeerName: "Bob", Kind: "file", Content: "a.txt"},
	}
	for _, entry := range entries {
		if err := store.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.History("192.168.1.2", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].PeerName != "Bob" {
		t.Fatalf("unexpected history: %#v", history)
	}
	users, err := store.SearchUsers("bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].IP != "192.168.1.2" || users[0].Count != 2 {
		t.Fatalf("unexpected users: %#v", users)
	}
}

func TestHistoryLimitKeepsNewestEntries(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"one", "two", "three"} {
		if err := store.Append(Entry{PeerIP: "127.0.0.1", Kind: "msg", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.History("127.0.0.1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Content != "two" || entries[1].Content != "three" {
		t.Fatalf("unexpected limited history: %#v", entries)
	}
}
