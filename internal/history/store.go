package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Time      time.Time `json:"time"`
	Direction string    `json:"direction"`
	PeerIP    string    `json:"peer_ip"`
	PeerName  string    `json:"peer_name,omitempty"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	SavedPath string    `json:"saved_path,omitempty"`
}

type User struct {
	IP       string
	Name     string
	LastSeen time.Time
	Count    int
}

type Store struct {
	path string
	mu   sync.Mutex
}

func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".feiq-cli", "history.jsonl")
	}
	return filepath.Join(home, ".feiq-cli", "history.jsonl")
}

func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return &Store{path: path}, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Append(entry Entry) error {
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) History(peerIP string, limit int) ([]Entry, error) {
	entries, err := s.readAll()
	if err != nil {
		return nil, err
	}
	matched := make([]Entry, 0)
	for _, entry := range entries {
		if entry.PeerIP == peerIP {
			matched = append(matched, entry)
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched, nil
}

func (s *Store) SearchUsers(query string) ([]User, error) {
	entries, err := s.readAll()
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	users := make(map[string]User)
	for _, entry := range entries {
		if entry.PeerIP == "" {
			continue
		}
		user := users[entry.PeerIP]
		user.IP = entry.PeerIP
		user.Count++
		if entry.Time.After(user.LastSeen) {
			user.LastSeen = entry.Time
			if entry.PeerName != "" {
				user.Name = entry.PeerName
			}
		} else if user.Name == "" && entry.PeerName != "" {
			user.Name = entry.PeerName
		}
		users[entry.PeerIP] = user
	}
	result := make([]User, 0, len(users))
	for _, user := range users {
		if query == "" || strings.Contains(strings.ToLower(user.IP), query) || strings.Contains(strings.ToLower(user.Name), query) {
			result = append(result, user)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastSeen.After(result[j].LastSeen) })
	return result, nil
}

func (s *Store) RecentTargets(limit int) ([]string, error) {
	entries, err := s.readAll()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var targets []string
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.Direction != "out" || entry.PeerIP == "" || seen[entry.PeerIP] {
			continue
		}
		seen[entry.PeerIP] = true
		targets = append(targets, entry.PeerIP)
		if limit > 0 && len(targets) >= limit {
			break
		}
	}
	return targets, nil
}

func (s *Store) readAll() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var entries []Entry
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}
