package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mlctrez/servicego"
)

// APIHandler handles REST API requests on the admin host under /api/.
type APIHandler struct {
	store     *EndpointStore
	dnsClient DNSClient
	logger    servicego.LoggerContainer
}

// ServeHTTP routes API requests:
//
//	GET    /api/endpoints           → handleList
//	POST   /api/endpoints           → handleAdd
//	DELETE /api/endpoints/{hostname} → handleRemove
//	else                            → 404
func (h *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")

	switch {
	case path == "/api/endpoints" && r.Method == http.MethodGet:
		h.handleList(w, r)
	case path == "/api/endpoints" && r.Method == http.MethodPost:
		h.handleAdd(w, r)
	case strings.HasPrefix(path, "/api/endpoints/") && r.Method == http.MethodDelete:
		h.handleRemove(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// handleList responds with a JSON array of all endpoints.
func (h *APIHandler) handleList(w http.ResponseWriter, _ *http.Request) {
	endpoints := h.store.All()
	writeJSON(w, http.StatusOK, endpoints)
}

// handleAdd decodes a JSON body, validates it, adds the endpoint to the store,
// and optionally performs Route53 DNS operations.
func (h *APIHandler) handleAdd(w http.ResponseWriter, r *http.Request) {
	var ep Endpoint
	if err := json.NewDecoder(r.Body).Decode(&ep); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	if ep.Host == "" || ep.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostname and url are required"})
		return
	}

	// Check DNS before adding if Route53 is configured.
	var dnsWarning string
	if h.dnsClient != nil {
		rec, err := h.dnsClient.LookupRecord(r.Context(), ep.Host)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"error": "DNS lookup failed: " + err.Error(),
			})
			return
		}
		if rec != nil && rec.Exists {
			if rec.Target != h.dnsClient.CNAMETarget() {
				dnsWarning = "DNS record exists pointing to " + rec.Target
			}
			// If already pointing to correct target, skip creation below.
		}
	}

	if err := h.store.Add(ep); err != nil {
		if strings.Contains(err.Error(), "invalid backend URL") ||
			strings.Contains(err.Error(), "must not be empty") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist endpoint"})
		return
	}

	// Create DNS record if Route53 is configured and no conflict.
	if h.dnsClient != nil && dnsWarning == "" {
		if err := h.dnsClient.CreateCNAME(r.Context(), ep.Host); err != nil {
			h.logger.Log().Warningf("Route53 CreateCNAME failed for %s: %v", ep.Host, err)
		}
	}

	resp := map[string]interface{}{
		"host":      ep.Host,
		"url":       ep.URL,
		"skipToken": ep.SkipToken,
	}
	if dnsWarning != "" {
		resp["warning"] = dnsWarning
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRemove extracts the hostname from the URL path, removes the endpoint,
// and optionally performs Route53 DNS cleanup.
func (h *APIHandler) handleRemove(w http.ResponseWriter, r *http.Request) {
	hostname := strings.TrimPrefix(r.URL.Path, "/api/endpoints/")
	hostname = strings.TrimSuffix(hostname, "/")

	if hostname == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostname is required"})
		return
	}

	if err := h.store.Remove(hostname); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "endpoint not found"})
		return
	}

	// Remove DNS record if Route53 is configured.
	if h.dnsClient != nil {
		if err := h.dnsClient.RemoveCNAME(r.Context(), hostname); err != nil {
			h.logger.Log().Warningf("Route53 RemoveCNAME failed for %s: %v", hostname, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"deleted": hostname})
}

// writeJSON encodes v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
