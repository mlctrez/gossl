package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

// genHost generates a valid hostname like "a.example.com".
func genHost(t *rapid.T) string {
	// Generate 1-3 labels separated by dots.
	n := rapid.IntRange(1, 3).Draw(t, "labelCount")
	labels := make([]string, n)
	for i := range labels {
		labels[i] = rapid.StringMatching(`[a-z][a-z0-9]{0,9}`).Draw(t, fmt.Sprintf("label%d", i))
	}
	// Build dotted hostname from labels.
	host := labels[0]
	for _, l := range labels[1:] {
		host += "." + l
	}
	return host
}

// genURL generates a valid backend URL like "http://10.0.0.1:9000".
func genURL(t *rapid.T) string {
	scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
	host := rapid.StringMatching(`[a-z0-9]{1,10}`).Draw(t, "urlHost")
	port := rapid.IntRange(1, 65535).Draw(t, "port")
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// genEndpoint generates a valid Endpoint with unique host within a test case.
func genEndpoint(t *rapid.T) Endpoint {
	return Endpoint{
		Host:      genHost(t),
		URL:       genURL(t),
		SkipToken: rapid.Bool().Draw(t, "skipToken"),
	}
}

// Feature: runtime-endpoint-api, Property 2: Add then list returns all endpoints
func TestProperty_AddThenList(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Create an empty store backed by a temp file.
		dir, dirErr := os.MkdirTemp("", "store-addlist-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		store := &EndpointStore{filePath: filePath}

		// Generate a random sequence of valid endpoints with unique hosts.
		n := rapid.IntRange(0, 20).Draw(t, "count")
		seen := map[string]bool{}
		var expected []Endpoint
		for i := 0; i < n; i++ {
			e := genEndpoint(t)
			if seen[e.Host] {
				continue
			}
			seen[e.Host] = true

			if err := store.Add(e); err != nil {
				t.Fatalf("Add(%+v) failed: %v", e, err)
			}
			expected = append(expected, e)
		}

		// All() should return exactly the added set.
		got := store.All()

		if len(expected) == 0 && len(got) == 0 {
			return
		}

		if len(got) != len(expected) {
			t.Fatalf("length mismatch: expected %d, got %d", len(expected), len(got))
		}

		// Build a map from host to endpoint for order-independent comparison.
		expectedMap := make(map[string]Endpoint, len(expected))
		for _, e := range expected {
			expectedMap[e.Host] = e
		}

		for _, g := range got {
			e, ok := expectedMap[g.Host]
			if !ok {
				t.Fatalf("unexpected endpoint in store: %+v", g)
			}
			if !reflect.DeepEqual(e, g) {
				t.Fatalf("endpoint mismatch for host %q:\n  expected: %+v\n  got:      %+v", g.Host, e, g)
			}
		}
	})
}

// Feature: runtime-endpoint-api, Property 1: Endpoint store round-trip (serialization)
func TestProperty_StoreRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random slice of endpoints with unique hosts.
		n := rapid.IntRange(0, 20).Draw(t, "count")
		seen := map[string]bool{}
		var endpoints []Endpoint
		for i := 0; i < n; i++ {
			e := genEndpoint(t)
			// Ensure unique hosts within this test case.
			if seen[e.Host] {
				continue
			}
			seen[e.Host] = true
			endpoints = append(endpoints, e)
		}

		// Write endpoints to a temp file via EndpointStore.
		dir, dirErr := os.MkdirTemp("", "store-roundtrip-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		store := &EndpointStore{
			endpoints: endpoints,
			filePath:  filePath,
		}

		// Acquire write lock as save() expects caller to hold it.
		store.mu.Lock()
		err := store.save()
		store.mu.Unlock()
		if err != nil {
			t.Fatalf("save failed: %v", err)
		}

		// Verify the file was written.
		if _, err := os.Stat(filePath); err != nil {
			t.Fatalf("file not created: %v", err)
		}

		// Load into a new store from the same file.
		loaded := &EndpointStore{filePath: filePath}
		if err := loaded.load(); err != nil {
			t.Fatalf("load failed: %v", err)
		}

		// Assert deep equality.
		got := loaded.All()

		// Both nil and empty should be treated as equivalent (zero endpoints).
		if len(endpoints) == 0 && len(got) == 0 {
			return
		}

		if !reflect.DeepEqual(endpoints, got) {
			t.Fatalf("round-trip mismatch:\n  wrote: %+v\n  read:  %+v", endpoints, got)
		}
	})
}

// Feature: runtime-endpoint-api, Property 3: Add then remove restores prior state
// **Validates: Requirements 5.1**
func TestProperty_AddThenRemove(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Create an empty store backed by a temp file.
		dir, dirErr := os.MkdirTemp("", "store-addremove-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		store := &EndpointStore{filePath: filePath}

		// Optionally seed the store with some initial endpoints (unique hosts).
		seedCount := rapid.IntRange(0, 10).Draw(t, "seedCount")
		seenHosts := map[string]bool{}
		for i := 0; i < seedCount; i++ {
			e := genEndpoint(t)
			if seenHosts[e.Host] {
				continue
			}
			seenHosts[e.Host] = true
			if err := store.Add(e); err != nil {
				t.Fatalf("seed Add(%+v) failed: %v", e, err)
			}
		}

		// Snapshot the store state before adding the new endpoint.
		before := store.All()

		// Generate a random new endpoint whose host is NOT already in the store.
		newEndpoint := genEndpoint(t)
		for seenHosts[newEndpoint.Host] {
			newEndpoint = genEndpoint(t)
		}

		// Add the new endpoint.
		if err := store.Add(newEndpoint); err != nil {
			t.Fatalf("Add(%+v) failed: %v", newEndpoint, err)
		}

		// Remove it by hostname.
		if err := store.Remove(newEndpoint.Host); err != nil {
			t.Fatalf("Remove(%q) failed: %v", newEndpoint.Host, err)
		}

		// Assert All() matches the snapshot from before the add.
		after := store.All()

		// Handle nil vs empty slice edge case.
		if len(before) == 0 && len(after) == 0 {
			return
		}

		// Order-independent comparison: build maps by host.
		beforeMap := make(map[string]Endpoint, len(before))
		for _, e := range before {
			beforeMap[e.Host] = e
		}
		afterMap := make(map[string]Endpoint, len(after))
		for _, e := range after {
			afterMap[e.Host] = e
		}

		if !reflect.DeepEqual(beforeMap, afterMap) {
			t.Fatalf("store state changed after add+remove:\n  before: %+v\n  after:  %+v", before, after)
		}
	})
}

// Feature: runtime-endpoint-api, Property 5: Update overwrites existing endpoint
// **Validates: Requirements 4.2**
func TestProperty_UpdateOverwrites(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Create an empty store backed by a temp file.
		dir, dirErr := os.MkdirTemp("", "store-update-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		store := &EndpointStore{filePath: filePath}

		// Generate a random valid endpoint and add it to the store.
		original := genEndpoint(t)
		if err := store.Add(original); err != nil {
			t.Fatalf("Add(%+v) failed: %v", original, err)
		}

		// Generate a different URL (ensure it differs from the original).
		newURL := genURL(t)
		for newURL == original.URL {
			newURL = genURL(t)
		}

		// Create a new endpoint with the same host but the new URL (and potentially different skipToken).
		updated := Endpoint{
			Host:      original.Host,
			URL:       newURL,
			SkipToken: rapid.Bool().Draw(t, "newSkipToken"),
		}

		// Add the updated endpoint to the store.
		if err := store.Add(updated); err != nil {
			t.Fatalf("Add(%+v) failed: %v", updated, err)
		}

		// Assert that All() returns exactly one entry.
		got := store.All()
		if len(got) != 1 {
			t.Fatalf("expected 1 endpoint, got %d: %+v", len(got), got)
		}

		// Assert that the single entry matches the updated endpoint.
		if got[0].Host != updated.Host {
			t.Fatalf("host mismatch: expected %q, got %q", updated.Host, got[0].Host)
		}
		if got[0].URL != updated.URL {
			t.Fatalf("URL mismatch: expected %q, got %q", updated.URL, got[0].URL)
		}
		if got[0].SkipToken != updated.SkipToken {
			t.Fatalf("skipToken mismatch: expected %v, got %v", updated.SkipToken, got[0].SkipToken)
		}
	})
}

// Feature: runtime-endpoint-api, Property 6: Invalid URL rejection
// **Validates: Requirements 4.3, 4.4**
func TestProperty_InvalidURLRejection(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Create a store backed by a temp file and seed it with some valid endpoints.
		dir, dirErr := os.MkdirTemp("", "store-invalidurl-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		store := &EndpointStore{filePath: filePath}

		// Seed the store with 0-5 valid endpoints.
		seedCount := rapid.IntRange(0, 5).Draw(t, "seedCount")
		seenHosts := map[string]bool{}
		for i := 0; i < seedCount; i++ {
			e := genEndpoint(t)
			if seenHosts[e.Host] {
				continue
			}
			seenHosts[e.Host] = true
			if err := store.Add(e); err != nil {
				t.Fatalf("seed Add(%+v) failed: %v", e, err)
			}
		}

		// Snapshot the store state before attempting the invalid add.
		before := store.All()

		// Generate an invalid URL string using one of several strategies.
		invalidURL := rapid.OneOf(
			// Empty string
			rapid.Just(""),
			// No scheme (just a hostname-like string)
			rapid.StringMatching(`[a-z][a-z0-9]{0,15}`),
			// String with spaces
			rapid.Map(rapid.StringMatching(`[a-z]{1,5}`), func(s string) string {
				return s + " " + s
			}),
			// Scheme-less URL with path
			rapid.StringMatching(`[a-z]{1,8}/[a-z]{1,8}`),
			// Just a colon
			rapid.Just(":"),
			// Just slashes
			rapid.Just("///"),
		).Draw(t, "invalidURL")

		// Use a valid host for the endpoint.
		host := genHost(t)
		for seenHosts[host] {
			host = genHost(t)
		}

		// Attempt to add an endpoint with the invalid URL.
		err := store.Add(Endpoint{
			Host:      host,
			URL:       invalidURL,
			SkipToken: rapid.Bool().Draw(t, "skipToken"),
		})

		// Add() must return a non-nil error for invalid URLs.
		if err == nil {
			t.Fatalf("expected error for invalid URL %q, but Add() returned nil", invalidURL)
		}

		// The store must be unchanged after the failed add.
		after := store.All()

		if len(before) == 0 && len(after) == 0 {
			return
		}

		if len(before) != len(after) {
			t.Fatalf("store length changed after failed add: before=%d, after=%d", len(before), len(after))
		}

		beforeMap := make(map[string]Endpoint, len(before))
		for _, e := range before {
			beforeMap[e.Host] = e
		}
		afterMap := make(map[string]Endpoint, len(after))
		for _, e := range after {
			afterMap[e.Host] = e
		}

		if !reflect.DeepEqual(beforeMap, afterMap) {
			t.Fatalf("store state changed after failed add:\n  before: %+v\n  after:  %+v", before, after)
		}
	})
}

// Feature: runtime-endpoint-api, Property 6: Invalid URL rejection (empty host)
// **Validates: Requirements 4.4**
func TestProperty_InvalidURLRejection_EmptyHost(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Create a store backed by a temp file and seed it.
		dir, dirErr := os.MkdirTemp("", "store-emptyhost-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		store := &EndpointStore{filePath: filePath}

		// Seed with 0-5 valid endpoints.
		seedCount := rapid.IntRange(0, 5).Draw(t, "seedCount")
		seenHosts := map[string]bool{}
		for i := 0; i < seedCount; i++ {
			e := genEndpoint(t)
			if seenHosts[e.Host] {
				continue
			}
			seenHosts[e.Host] = true
			if err := store.Add(e); err != nil {
				t.Fatalf("seed Add(%+v) failed: %v", e, err)
			}
		}

		// Snapshot the store state.
		before := store.All()

		// Attempt to add with empty host and a valid URL.
		err := store.Add(Endpoint{
			Host:      "",
			URL:       genURL(t),
			SkipToken: rapid.Bool().Draw(t, "skipToken"),
		})

		// Must return error for empty host.
		if err == nil {
			t.Fatalf("expected error for empty host, but Add() returned nil")
		}

		// Store must be unchanged.
		after := store.All()

		if len(before) == 0 && len(after) == 0 {
			return
		}

		if len(before) != len(after) {
			t.Fatalf("store length changed after failed add: before=%d, after=%d", len(before), len(after))
		}

		beforeMap := make(map[string]Endpoint, len(before))
		for _, e := range before {
			beforeMap[e.Host] = e
		}
		afterMap := make(map[string]Endpoint, len(after))
		for _, e := range after {
			afterMap[e.Host] = e
		}

		if !reflect.DeepEqual(beforeMap, afterMap) {
			t.Fatalf("store state changed after failed add:\n  before: %+v\n  after:  %+v", before, after)
		}
	})
}

// Feature: runtime-endpoint-api, Property 8: Load validation rejects invalid entries
// **Validates: Requirements 12.2**
func TestProperty_LoadValidation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a mix of valid and invalid endpoint entries.
		validCount := rapid.IntRange(0, 10).Draw(t, "validCount")
		invalidCount := rapid.IntRange(1, 10).Draw(t, "invalidCount")

		seenHosts := map[string]bool{}
		var validEntries []Endpoint

		// Generate valid entries with unique hosts.
		for i := 0; i < validCount; i++ {
			e := genEndpoint(t)
			if seenHosts[e.Host] {
				continue
			}
			seenHosts[e.Host] = true
			validEntries = append(validEntries, e)
		}

		// Build the full JSON array mixing valid and invalid entries.
		type rawEntry struct {
			Host      string `json:"host"`
			URL       string `json:"url"`
			SkipToken bool   `json:"skipToken,omitempty"`
		}

		var allEntries []rawEntry

		// Add valid entries.
		for _, e := range validEntries {
			allEntries = append(allEntries, rawEntry{Host: e.Host, URL: e.URL, SkipToken: e.SkipToken})
		}

		// Generate invalid entries using different strategies.
		for i := 0; i < invalidCount; i++ {
			strategy := rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("invalidStrategy_%d", i))
			switch strategy {
			case 0:
				// Empty hostname with valid URL.
				allEntries = append(allEntries, rawEntry{Host: "", URL: genURL(t), SkipToken: rapid.Bool().Draw(t, fmt.Sprintf("inv_skip_%d", i))})
			case 1:
				// Valid hostname with URL missing scheme (just a bare word).
				host := genHost(t)
				for seenHosts[host] {
					host = genHost(t)
				}
				bareWord := rapid.StringMatching(`[a-z][a-z0-9]{1,10}`).Draw(t, fmt.Sprintf("bareWord_%d", i))
				allEntries = append(allEntries, rawEntry{Host: host, URL: bareWord, SkipToken: rapid.Bool().Draw(t, fmt.Sprintf("inv_skip2_%d", i))})
			case 2:
				// Valid hostname with empty URL.
				host := genHost(t)
				for seenHosts[host] {
					host = genHost(t)
				}
				allEntries = append(allEntries, rawEntry{Host: host, URL: "", SkipToken: rapid.Bool().Draw(t, fmt.Sprintf("inv_skip3_%d", i))})
			}
		}

		// Marshal to JSON and write to a temp file.
		data, err := json.Marshal(allEntries)
		if err != nil {
			t.Fatalf("failed to marshal test JSON: %v", err)
		}

		dir, dirErr := os.MkdirTemp("", "store-loadvalidation-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		if err := os.WriteFile(filePath, data, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		// Load the store from the file.
		store := &EndpointStore{filePath: filePath}
		loadErr := store.load()

		// load() should return nil because the JSON is well-formed.
		if loadErr != nil {
			t.Fatalf("load() returned error for well-formed JSON: %v", loadErr)
		}

		// The store should contain exactly the valid entries.
		got := store.All()

		if len(validEntries) == 0 && len(got) == 0 {
			return
		}

		if len(got) != len(validEntries) {
			t.Fatalf("expected %d valid endpoints, got %d\n  valid: %+v\n  got: %+v",
				len(validEntries), len(got), validEntries, got)
		}

		// Build map for order-independent comparison.
		expectedMap := make(map[string]Endpoint, len(validEntries))
		for _, e := range validEntries {
			expectedMap[e.Host] = e
		}

		for _, g := range got {
			e, ok := expectedMap[g.Host]
			if !ok {
				t.Fatalf("unexpected endpoint in store after load: %+v", g)
			}
			if g.URL != e.URL {
				t.Fatalf("URL mismatch for host %q: expected %q, got %q", g.Host, e.URL, g.URL)
			}
			if g.SkipToken != e.SkipToken {
				t.Fatalf("skipToken mismatch for host %q: expected %v, got %v", g.Host, e.SkipToken, g.SkipToken)
			}
		}
	})
}

// Feature: runtime-endpoint-api, Property 8: Load validation rejects malformed JSON
// **Validates: Requirements 12.2**
func TestProperty_LoadValidation_MalformedJSON(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate some non-JSON content.
		malformed := rapid.OneOf(
			rapid.Just("{not json at all}"),
			rapid.Just("[{\"host\": broken}]"),
			rapid.Just("just a string"),
			rapid.Just("{]"),
			rapid.Just(""),
		).Draw(t, "malformedJSON")

		dir, dirErr := os.MkdirTemp("", "store-malformed-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		if err := os.WriteFile(filePath, []byte(malformed), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		store := &EndpointStore{filePath: filePath}
		loadErr := store.load()

		// load() must return an error for malformed JSON.
		if loadErr == nil {
			t.Fatalf("expected error for malformed JSON %q, but load() returned nil", malformed)
		}
	})
}

// Feature: runtime-endpoint-api, Property 4: Host policy tracks store membership
// **Validates: Requirements 4.5, 5.3**
func TestProperty_HostPolicyTracksStore(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Create an empty store backed by a temp file.
		dir, dirErr := os.MkdirTemp("", "store-hostpolicy-*")
		if dirErr != nil {
			t.Fatalf("failed to create temp dir: %v", dirErr)
		}
		defer os.RemoveAll(dir)
		filePath := filepath.Join(dir, "endpoints.json")

		store := &EndpointStore{filePath: filePath}

		// Create a Service with the store.
		svc := &Service{store: store}

		ctx := context.Background()

		// Generate a set of endpoints to add.
		addCount := rapid.IntRange(1, 15).Draw(t, "addCount")
		seenHosts := map[string]bool{}
		var addedHosts []string

		for i := 0; i < addCount; i++ {
			e := genEndpoint(t)
			if seenHosts[e.Host] {
				continue
			}
			seenHosts[e.Host] = true
			if err := store.Add(e); err != nil {
				t.Fatalf("Add(%+v) failed: %v", e, err)
			}
			addedHosts = append(addedHosts, e.Host)
		}

		// Assert hostPolicy returns nil for all added hosts.
		for _, host := range addedHosts {
			if err := svc.hostPolicy(ctx, host); err != nil {
				t.Fatalf("hostPolicy(%q) returned error for host in store: %v", host, err)
			}
		}

		// Generate a host that was never added and assert hostPolicy returns error.
		neverAdded := genHost(t)
		for seenHosts[neverAdded] {
			neverAdded = genHost(t)
		}
		if err := svc.hostPolicy(ctx, neverAdded); err == nil {
			t.Fatalf("hostPolicy(%q) returned nil for host NOT in store", neverAdded)
		}

		// Remove a random subset of added hosts and verify hostPolicy rejects them.
		var removedHosts []string
		var keptHosts []string
		for _, host := range addedHosts {
			if rapid.Bool().Draw(t, fmt.Sprintf("remove_%s", host)) {
				if err := store.Remove(host); err != nil {
					t.Fatalf("Remove(%q) failed: %v", host, err)
				}
				removedHosts = append(removedHosts, host)
			} else {
				keptHosts = append(keptHosts, host)
			}
		}

		// Removed hosts should be rejected by hostPolicy.
		for _, host := range removedHosts {
			if err := svc.hostPolicy(ctx, host); err == nil {
				t.Fatalf("hostPolicy(%q) returned nil for removed host", host)
			}
		}

		// Kept hosts should still be accepted by hostPolicy.
		for _, host := range keptHosts {
			if err := svc.hostPolicy(ctx, host); err != nil {
				t.Fatalf("hostPolicy(%q) returned error for host still in store: %v", host, err)
			}
		}
	})
}
