package main

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/mlctrez/servicego"
)

//go:embed templates/*.html static/*.css static/*.svg
var webContent embed.FS

// WebHandler handles the embedded web UI on the admin host.
type WebHandler struct {
	store     *EndpointStore
	dnsClient DNSClient
	templates *template.Template
	logger    servicego.LoggerContainer
}

// endpointView is the per-row view model for the endpoint list template.
type endpointView struct {
	Host      string
	URL       string
	SkipToken bool
	DNSRecord *DNSRecord
}

// pageData is the top-level data passed to the endpoints template.
type pageData struct {
	Endpoints     []endpointView
	DNSEnabled    bool
	Error         string
	Warning       string
	EditHost      string
	EditURL       string
	EditSkipToken bool
}

// NewWebHandler creates a WebHandler with parsed embedded templates.
func NewWebHandler(store *EndpointStore, dnsClient DNSClient, logger servicego.LoggerContainer) *WebHandler {
	tmpl := template.Must(template.ParseFS(webContent, "templates/layout.html", "templates/endpoints.html"))
	return &WebHandler{store: store, dnsClient: dnsClient, templates: tmpl, logger: logger}
}

// ServeHTTP routes web UI requests: static files at /static/, everything else
// renders the endpoint list page.
func (h *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Serve embedded static files.
	if strings.HasPrefix(r.URL.Path, "/static/") {
		staticFS, err := fs.Sub(webContent, "static")
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))).ServeHTTP(w, r)
		return
	}

	switch r.URL.Path {
	case "/add":
		h.handleAdd(w, r)
	case "/remove":
		h.handleRemove(w, r)
	case "/edit":
		h.handleEdit(w, r)
	default:
		h.handleList(w, r)
	}
}

// handleList renders the endpoint list page.
func (h *WebHandler) handleList(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, pageData{})
}

// handleAdd processes the add/update form submission by calling the API endpoint,
// then redirects back to the list page.
func (h *WebHandler) handleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	host := strings.TrimSpace(r.FormValue("host"))
	backendURL := strings.TrimSpace(r.FormValue("url"))
	skipToken := r.FormValue("skipToken") == "true"

	ep := Endpoint{Host: host, URL: backendURL, SkipToken: skipToken}

	if err := h.store.Add(ep); err != nil {
		h.renderPage(w, r, pageData{Error: err.Error()})
		return
	}

	// Optionally create DNS record.
	if h.dnsClient != nil {
		rec, err := h.dnsClient.LookupRecord(r.Context(), host)
		if err != nil {
			h.renderPage(w, r, pageData{Warning: "Endpoint added, but DNS lookup failed: " + err.Error()})
			return
		}
		if rec != nil && rec.Exists && rec.Target != h.dnsClient.CNAMETarget() {
			h.renderPage(w, r, pageData{Warning: "Endpoint added. DNS record exists pointing to " + rec.Target})
			return
		}
		if rec == nil || !rec.Exists {
			if err := h.dnsClient.CreateCNAME(r.Context(), host); err != nil {
				h.logger.Log().Warningf("Route53 CreateCNAME failed for %s: %v", host, err)
			}
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleRemove processes the remove form submission.
func (h *WebHandler) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	host := strings.TrimSpace(r.FormValue("host"))
	if err := h.store.Remove(host); err != nil {
		h.renderPage(w, r, pageData{Error: "Failed to remove: " + err.Error()})
		return
	}

	// Optionally remove DNS record.
	if h.dnsClient != nil {
		if err := h.dnsClient.RemoveCNAME(r.Context(), host); err != nil {
			h.logger.Log().Warningf("Route53 RemoveCNAME failed for %s: %v", host, err)
		}
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleEdit pre-fills the add form with the selected endpoint's values.
func (h *WebHandler) handleEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	h.renderPage(w, r, pageData{
		EditHost:      r.FormValue("host"),
		EditURL:       r.FormValue("url"),
		EditSkipToken: r.FormValue("skipToken") == "true",
	})
}

// renderPage builds the page data and executes the template.
func (h *WebHandler) renderPage(w http.ResponseWriter, r *http.Request, data pageData) {
	endpoints := h.store.All()
	views := make([]endpointView, len(endpoints))
	for i, ep := range endpoints {
		ev := endpointView{Host: ep.Host, URL: ep.URL, SkipToken: ep.SkipToken}
		if h.dnsClient != nil {
			rec, err := h.dnsClient.LookupRecord(r.Context(), ep.Host)
			if err == nil && rec != nil && rec.Exists {
				ev.DNSRecord = rec
			}
		}
		views[i] = ev
	}
	data.Endpoints = views
	data.DNSEnabled = h.dnsClient != nil

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.Execute(w, data); err != nil {
		h.logger.Log().Errorf("template execution error: %v", err)
	}
}
