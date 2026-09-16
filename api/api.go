package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// webdist holds the built SvelteKit app. The build output lands in web/build/
// and is copied here by `make build` (see the Makefile) because go:embed cannot
// reach outside the package directory.
//
// The `all:` prefix keeps dot-files — both the committed .gitkeep that makes
// this directive valid on a fresh checkout, and SvelteKit's own dot-prefixed
// asset directories.
//
//go:embed all:webdist
var webdist embed.FS

// maxBodyBytes caps a decoded request body. Descriptions are markdown wiki
// entries, so this is generous, but unbounded reads from an open port are not.
const maxBodyBytes = 1 << 20 // 1 MiB

// Server wires the store and config into the HTTP handlers.
type Server struct {
	cfg   Config
	store *Store
	// web is the embedded frontend rooted at webdist/, or nil when no frontend
	// has been built into this binary (the `make dev-api` case).
	web fs.FS
}

// NewServer builds the server and resolves the embedded frontend, if any.
func NewServer(cfg Config, store *Store) *Server {
	s := &Server{cfg: cfg, store: store}

	sub, err := fs.Sub(webdist, "webdist")
	if err != nil {
		log.Printf("places: embedded frontend unavailable: %v", err)
		return s
	}
	// Only treat the frontend as present if it actually has an entrypoint —
	// otherwise the binary was built without running the web build and every
	// non-API route would 404 with no explanation.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		log.Printf("places: no embedded frontend (run `make build`); serving API only")
		return s
	}
	s.web = sub
	return s
}

// ---------- response helpers ----------

// ListResponse is the paginated envelope returned by GET /api/places.
type ListResponse struct {
	Data       []Place `json:"data"`
	Total      int     `json:"total"`
	Page       int     `json:"page"`
	PerPage    int     `json:"per_page"`
	TotalPages int     `json:"total_pages"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already on the wire, so this can only be logged.
		log.Printf("places: write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps a store error onto the right status code, so handlers
// don't each re-derive the same mapping.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "place not found")
	case errors.Is(err, ErrTripNotFound):
		// The sentinel's own text is "trip not found", so an unwrapped miss reads
		// correctly and a wrapped one ("...: that place is not a stop on this
		// trip") keeps the detail.
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrInvalidTrip):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("places: store error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// decodeBody reads a JSON request body with a size cap and rejects unknown
// fields, so a typo'd key fails loudly instead of being silently dropped.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Reject trailing content so `{...}{...}` isn't quietly accepted as the
	// first object alone.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}

// ---------- routing ----------

// Handler builds the full route tree: /api/* for JSON, everything else for the
// embedded frontend.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.cors)

	r.Get("/health", s.handleHealth)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.Get("/tags", s.handleListTags)

		r.Route("/places", func(r chi.Router) {
			r.Get("/", s.handleListPlaces)
			r.Post("/", s.handleCreatePlace)
			r.Get("/slug/{slug}", s.handleGetPlaceBySlug)
			r.Get("/{id}", s.handleGetPlace)
			r.Put("/{id}", s.handleUpdatePlace)
			r.Delete("/{id}", s.handleDeletePlace)
		})

		r.Route("/trips", func(r chi.Router) {
			r.Get("/", s.handleListTrips)
			r.Post("/", s.handleCreateTrip)
			r.Get("/slug/{slug}", s.handleGetTripBySlug)
			r.Get("/{id}", s.handleGetTrip)
			r.Put("/{id}", s.handleUpdateTrip)
			r.Delete("/{id}", s.handleDeleteTrip)
			r.Post("/{id}/places", s.handleAddTripPlace)
			r.Delete("/{id}/places/{placeId}", s.handleRemoveTripPlace)
		})

		// Anything else under /api is a client mistake — answer as JSON rather
		// than falling through to the SPA and returning HTML to a fetch().
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "no such API endpoint")
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		})
	})

	r.NotFound(s.handleFrontend)

	return r
}

// cors permits cross-origin API calls from the SvelteKit dev server, which runs
// on a different port. Disabled unless CORS_ORIGIN is set, so the production
// single-binary deployment (same origin) stays closed by default.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := s.cfg.CORSOrigin
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Echoing a specific origin rather than "*" keeps credentialed requests
		// workable; Vary tells caches the response is origin-dependent.
		allow := origin
		if origin != "*" {
			w.Header().Add("Vary", "Origin")
			reqOrigin := r.Header.Get("Origin")
			if reqOrigin != "" && !originAllowed(origin, reqOrigin) {
				// Not an allowed origin: process the request without CORS
				// headers and let the browser block the response.
				next.ServeHTTP(w, r)
				return
			}
			if reqOrigin != "" {
				allow = reqOrigin
			}
		}

		w.Header().Set("Access-Control-Allow-Origin", allow)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "300")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed reports whether reqOrigin appears in the comma-separated
// allowlist.
func originAllowed(allowlist, reqOrigin string) bool {
	for _, o := range strings.Split(allowlist, ",") {
		if strings.EqualFold(strings.TrimSpace(o), reqOrigin) {
			return true
		}
	}
	return false
}

// ---------- handlers ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.Count()
	if err != nil {
		log.Printf("places: health check: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "error",
			"error":  "database unavailable",
		})
		return
	}
	trips, err := s.store.CountTrips()
	if err != nil {
		log.Printf("places: health check: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "error",
			"error":  "database unavailable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"places":   count,
		"trips":    trips,
		"frontend": s.web != nil,
	})
}

func (s *Server) handleListPlaces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := ListOptions{
		Page:    atoiDefault(q.Get("page"), 1),
		PerPage: atoiDefault(q.Get("per_page"), defaultPerPage),
		Tag:     q.Get("tag"),
		City:    q.Get("city"),
		Q:       q.Get("q"),
	}
	opts.normalize()

	places, total, err := s.store.List(opts)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Ceiling division; an empty result set is one (empty) page, not zero, so
	// the frontend's "page X of Y" never reads "1 of 0".
	totalPages := (total + opts.PerPage - 1) / opts.PerPage
	if totalPages == 0 {
		totalPages = 1
	}

	writeJSON(w, http.StatusOK, ListResponse{
		Data:       places,
		Total:      total,
		Page:       opts.Page,
		PerPage:    opts.PerPage,
		TotalPages: totalPages,
	})
}

func (s *Server) handleGetPlace(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.Get(chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleGetPlaceBySlug(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetBySlug(chi.URLParam(r, "slug"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreatePlace(w http.ResponseWriter, r *http.Request) {
	var in PlaceInput
	if err := decodeBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	p, err := s.store.Create(in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Location", "/api/places/"+p.ID)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdatePlace(w http.ResponseWriter, r *http.Request) {
	var in PlaceInput
	if err := decodeBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	p, err := s.store.Update(chi.URLParam(r, "id"), in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePlace(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(chi.URLParam(r, "id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.store.AllTags()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": tags, "total": len(tags)})
}

// ---------- trip handlers ----------

func (s *Server) handleListTrips(w http.ResponseWriter, r *http.Request) {
	trips, err := s.store.ListTrips()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": trips, "total": len(trips)})
}

func (s *Server) handleGetTrip(w http.ResponseWriter, r *http.Request) {
	trip, err := s.store.TripDetailByID(chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trip)
}

func (s *Server) handleGetTripBySlug(w http.ResponseWriter, r *http.Request) {
	trip, err := s.store.TripDetailBySlug(chi.URLParam(r, "slug"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trip)
}

func (s *Server) handleCreateTrip(w http.ResponseWriter, r *http.Request) {
	var in TripInput
	if err := decodeBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	trip, err := s.store.CreateTrip(in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Location", "/api/trips/"+trip.ID)
	writeJSON(w, http.StatusCreated, trip)
}

func (s *Server) handleUpdateTrip(w http.ResponseWriter, r *http.Request) {
	var in TripInput
	if err := decodeBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	trip, err := s.store.UpdateTrip(chi.URLParam(r, "id"), in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trip)
}

func (s *Server) handleDeleteTrip(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteTrip(chi.URLParam(r, "id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddTripPlace(w http.ResponseWriter, r *http.Request) {
	var in TripPlaceInput
	if err := decodeBody(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	stop, err := s.store.AddTripPlace(chi.URLParam(r, "id"), in)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, stop)
}

func (s *Server) handleRemoveTripPlace(w http.ResponseWriter, r *http.Request) {
	err := s.store.RemoveTripPlace(chi.URLParam(r, "id"), chi.URLParam(r, "placeId"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- frontend ----------

// handleFrontend serves the embedded SvelteKit build, falling back to
// index.html so client-side routes resolve on a hard refresh.
func (s *Server) handleFrontend(w http.ResponseWriter, r *http.Request) {
	if s.web == nil {
		writeError(w, http.StatusNotFound,
			"frontend not built into this binary — run `make build`, or use the SvelteKit dev server")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}

	f, err := s.web.Open(name)
	if err != nil {
		s.serveIndex(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		s.serveIndex(w, r)
		return
	}

	// Hashed build assets under _app/immutable/ are content-addressed and safe
	// to cache forever; everything else (including the shell) must revalidate
	// so a deploy is picked up immediately.
	if strings.HasPrefix(name, "_app/immutable/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	rs, ok := f.(io.ReadSeeker)
	if !ok {
		// Should not happen for embed.FS, but fall back to a plain copy rather
		// than failing the request.
		w.Header().Set("Content-Type", contentTypeFor(name))
		io.Copy(w, f)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), rs)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.web, "index.html")
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// contentTypeFor is the minimal fallback used only when http.ServeContent's own
// extension sniffing is unavailable.
func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
