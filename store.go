package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sync"
)

// Endpoint represents a mapping from a hostname to a backend URL.
type Endpoint struct {
	Host      string `json:"host"`
	URL       string `json:"url"`
	SkipToken bool   `json:"skipToken,omitempty"`
}

// EndpointStore provides thread-safe endpoint storage with JSON persistence.
type EndpointStore struct {
	mu        sync.RWMutex
	endpoints []Endpoint
	filePath  string
}

// NewEndpointStore creates an EndpointStore. If filePath exists on disk, it loads
// from the file. Otherwise it creates an empty store and persists it.
func NewEndpointStore(filePath string) (*EndpointStore, error) {
	s := &EndpointStore{filePath: filePath}

	if _, err := os.Stat(filePath); err == nil {
		if err := s.load(); err != nil {
			return nil, err
		}
		return s, nil
	}

	// File does not exist – create empty store and save.
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// All returns a snapshot copy of all endpoints.
func (s *EndpointStore) All() []Endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Endpoint, len(s.endpoints))
	copy(out, s.endpoints)
	return out
}

// Lookup returns the parsed URL for a hostname. Returns nil, false if not found.
func (s *EndpointStore) Lookup(host string) (*url.URL, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.endpoints {
		if e.Host == host {
			u, err := url.Parse(e.URL)
			if err != nil {
				return nil, false
			}
			return u, true
		}
	}
	return nil, false
}

// IsSkipToken returns whether a host should skip token validation.
func (s *EndpointStore) IsSkipToken(host string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.endpoints {
		if e.Host == host {
			return e.SkipToken
		}
	}
	return false
}

// Has returns whether a hostname exists in the store.
func (s *EndpointStore) Has(host string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.endpoints {
		if e.Host == host {
			return true
		}
	}
	return false
}

// Add adds or updates an endpoint and persists to disk. It validates the URL
// before adding. If the host already exists, the entry is updated (upsert).
func (s *EndpointStore) Add(e Endpoint) error {
	if e.Host == "" {
		return fmt.Errorf("hostname must not be empty")
	}
	if e.URL == "" {
		return fmt.Errorf("url must not be empty")
	}
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("invalid backend URL: %s", e.URL)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.endpoints {
		if existing.Host == e.Host {
			s.endpoints[i] = e
			return s.save()
		}
	}
	s.endpoints = append(s.endpoints, e)
	return s.save()
}

// Remove removes an endpoint by hostname and persists to disk.
// Returns an error if the hostname is not found.
func (s *EndpointStore) Remove(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, e := range s.endpoints {
		if e.Host == host {
			s.endpoints = append(s.endpoints[:i], s.endpoints[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("endpoint not found")
}

// save writes the current endpoints to disk as JSON. Caller must hold the write lock.
func (s *EndpointStore) save() error {
	data, err := json.MarshalIndent(s.endpoints, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal endpoints: %w", err)
	}
	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write endpoints file: %w", err)
	}
	return nil
}

// load reads the JSON file and populates the store. It validates each entry,
// rejecting entries with empty hostnames or unparseable URLs. Returns an error
// for malformed JSON (caller should log and exit).
func (s *EndpointStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read endpoints file: %w", err)
	}

	var raw []Endpoint
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("malformed JSON in %s: %w", s.filePath, err)
	}

	var valid []Endpoint
	for _, e := range raw {
		if e.Host == "" {
			continue
		}
		u, parseErr := url.Parse(e.URL)
		if parseErr != nil || u.Scheme == "" {
			continue
		}
		valid = append(valid, e)
	}
	s.endpoints = valid
	return nil
}
