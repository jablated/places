package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a place id or slug is unknown. The API maps it to a 404.
var ErrNotFound = errors.New("place not found")

// ErrInvalid is returned when a write violates a field constraint (blank name,
// out-of-range coordinates, over-long field). The API maps it to a 400 and
// surfaces the wrapped message, so wrap it with something a human can act on.
var ErrInvalid = errors.New("invalid place")

const (
	maxNameLen        = 300
	maxSlugLen        = 200
	maxDescriptionLen = 200_000
	maxSourceURLLen   = 2048
	maxTagLen         = 64
	maxTagsPerPlace   = 30

	defaultCity = "New York"
)

// Place is a single wiki entry. Lat/Lng are pointers because a place may be
// known by name and neighborhood before anyone has pinned exact coordinates —
// the map view simply skips those rather than dropping a marker at (0,0) in the
// Gulf of Guinea.
type Place struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Description  string   `json:"description"`
	SourceURL    string   `json:"source_url"`
	Lat          *float64 `json:"lat"`
	Lng          *float64 `json:"lng"`
	City         string   `json:"city"`
	Neighborhood string   `json:"neighborhood"`
	Tags         []string `json:"tags"`
	Visited      bool     `json:"visited"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// PlaceInput is the writable shape accepted by POST/PUT. Every field is a
// pointer so PUT can tell "field omitted, leave it alone" apart from "field
// explicitly set to the zero value" — without that distinction you can never
// clear a neighborhood or un-visit a place.
type PlaceInput struct {
	Name         *string   `json:"name"`
	Slug         *string   `json:"slug"`
	Description  *string   `json:"description"`
	SourceURL    *string   `json:"source_url"`
	Lat          *float64  `json:"lat"`
	Lng          *float64  `json:"lng"`
	City         *string   `json:"city"`
	Neighborhood *string   `json:"neighborhood"`
	Tags         *[]string `json:"tags"`
	Visited      *bool     `json:"visited"`
}

// TagCount is a distinct tag and how many places carry it. AllTags sorts by
// count desc then tag asc, so the frontend can take the head of the slice as
// "popular tags" without re-sorting.
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// ListOptions are the filters and pagination for a list query. Zero values mean
// "unfiltered"; Page/PerPage are normalized by the caller (see normalize).
type ListOptions struct {
	Page    int
	PerPage int
	Tags    []string
	City    string
	Q       string

	// Optional bounding box. The filter applies only when all four are set;
	// places without coordinates never match it.
	SWLat, SWLng, NELat, NELng *float64
}

const (
	defaultPerPage = 20
	maxPerPage     = 100
)

func (o *ListOptions) normalize() {
	if o.Page < 1 {
		o.Page = 1
	}
	if o.PerPage < 1 {
		o.PerPage = defaultPerPage
	}
	if o.PerPage > maxPerPage {
		o.PerPage = maxPerPage
	}
}

const schema = `
CREATE TABLE IF NOT EXISTS places (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	slug         TEXT NOT NULL UNIQUE,
	description  TEXT NOT NULL DEFAULT '',
	source_url   TEXT NOT NULL DEFAULT '',
	lat          REAL,
	lng          REAL,
	city         TEXT NOT NULL DEFAULT 'New York',
	neighborhood TEXT NOT NULL DEFAULT '',
	tags         TEXT NOT NULL DEFAULT '[]',
	visited      INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_places_created_at ON places(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_places_city ON places(city);

CREATE TABLE IF NOT EXISTS trips (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	slug          TEXT UNIQUE NOT NULL,
	description   TEXT,
	trip_type     TEXT DEFAULT 'trip',
	tags          TEXT DEFAULT '[]',
	duration_days INTEGER,
	source_url    TEXT,
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_trips_created_at ON trips(created_at DESC);

CREATE TABLE IF NOT EXISTS trip_places (
	trip_id     TEXT NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
	place_id    TEXT NOT NULL REFERENCES places(id) ON DELETE CASCADE,
	route       TEXT,
	route_order INTEGER DEFAULT 0,
	stop_notes  TEXT,
	PRIMARY KEY (trip_id, place_id)
);
-- The trip_id half of the primary key already indexes "stops of this trip";
-- this covers the other direction, used by the cascade on place delete.
CREATE INDEX IF NOT EXISTS idx_trip_places_place ON trip_places(place_id);
`

// Store owns the single SQLite connection. All methods are safe for concurrent
// use: the connection pool is capped at one, so writes serialize rather than
// racing for the database lock.
type Store struct {
	conn *sql.DB
	path string
}

// OpenStore opens (creating if needed) the places database under dataDir and
// applies the schema. Safe to call against an existing database — the schema is
// all IF NOT EXISTS.
func OpenStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir data dir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "places.db")

	// journal_mode=WAL for concurrent readers alongside the single writer,
	// busy_timeout so a contended write backs off instead of erroring out,
	// synchronous=NORMAL as the usual durability/throughput tradeoff under WAL,
	// foreign_keys=ON for correctness if relations get added later.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)",
		dbPath,
	)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single writer: serialize all access through one connection.
	conn.SetMaxOpenConns(1)

	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return &Store{conn: conn, path: dbPath}, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.conn.Close() }

// Path is the on-disk location of the database file.
func (s *Store) Path() string { return s.path }

// ---------- helpers ----------

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

// slugDrop deletes characters that should vanish rather than become a
// separator, so "Katz's" slugs to "katzs" and not "katz-s". Covers the straight
// apostrophe plus the curly quotes that arrive when a name is pasted from a
// web page.
var slugDrop = strings.NewReplacer("'", "", "\u2018", "", "\u2019", "", "\u02bc", "")

// slugStrip collapses every remaining run of non-alphanumerics into one hyphen.
var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// slugify renders a name as a URL-friendly identifier: lowercase, non
// alphanumerics collapsed to single hyphens, trimmed. Returns "" if nothing
// usable survives (e.g. a name that is entirely emoji) — callers fall back to a
// generated slug in that case.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugDrop.Replace(s)
	s = slugStrip.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > maxSlugLen {
		s = strings.Trim(s[:maxSlugLen], "-")
	}
	return s
}

// normalizeTag trims and lowercases a tag so "Food", " food " and "FOOD" are
// one tag. Length is enforced separately by normalizeTags.
func normalizeTag(t string) string { return strings.ToLower(strings.TrimSpace(t)) }

// normalizeTags cleans, dedupes and validates a tag set, preserving first-seen
// order so the author's ordering survives a round trip.
func normalizeTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		t := normalizeTag(raw)
		if t == "" {
			continue
		}
		if len(t) > maxTagLen {
			return nil, fmt.Errorf("%w: tag %q exceeds %d bytes", ErrInvalid, t, maxTagLen)
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > maxTagsPerPlace {
		return nil, fmt.Errorf("%w: %d tags exceeds the limit of %d", ErrInvalid, len(out), maxTagsPerPlace)
	}
	return out, nil
}

// likeEscape escapes LIKE metacharacters (% and _) plus the escape character
// itself so a user search for "100%" matches literally. Pair with ESCAPE '\'.
// The backslash is replaced first (Replacer matches arguments in order) so an
// escaped % isn't double-escaped.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// validateCoords rejects out-of-range coordinates. Latitude and longitude must
// be supplied together — a lone latitude can't be mapped, and silently storing
// half a coordinate pair produces markers that are wrong rather than absent.
func validateCoords(lat, lng *float64) error {
	if (lat == nil) != (lng == nil) {
		return fmt.Errorf("%w: lat and lng must be set together", ErrInvalid)
	}
	if lat != nil && (*lat < -90 || *lat > 90) {
		return fmt.Errorf("%w: lat %v out of range [-90, 90]", ErrInvalid, *lat)
	}
	if lng != nil && (*lng < -180 || *lng > 180) {
		return fmt.Errorf("%w: lng %v out of range [-180, 180]", ErrInvalid, *lng)
	}
	return nil
}

func encodeTags(tags []string) string {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		// Marshalling []string cannot fail; fall back to an empty array rather
		// than propagating an impossible error up through every write path.
		return "[]"
	}
	return string(b)
}

// decodeTags parses the stored JSON array. A malformed value (hand-edited
// database, say) reads as no tags rather than failing the whole list query.
func decodeTags(raw string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil || tags == nil {
		return []string{}
	}
	return tags
}

func scanPlace(sc interface{ Scan(...any) error }) (Place, error) {
	var p Place
	var rawTags string
	var lat, lng sql.NullFloat64
	err := sc.Scan(
		&p.ID, &p.Name, &p.Slug, &p.Description, &p.SourceURL,
		&lat, &lng, &p.City, &p.Neighborhood, &rawTags,
		&p.Visited, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return Place{}, err
	}
	if lat.Valid {
		p.Lat = &lat.Float64
	}
	if lng.Valid {
		p.Lng = &lng.Float64
	}
	p.Tags = decodeTags(rawTags)
	return p, nil
}

const placeColumns = `id, name, slug, description, source_url, lat, lng, city, neighborhood, tags, visited, created_at, updated_at`

// uniqueSlug returns base, or base-2, base-3, ... if base is already taken by
// another place. excludeID lets an update keep its own slug without colliding
// with itself.
func (s *Store) uniqueSlug(base, excludeID string) (string, error) {
	return s.uniqueSlugIn("places", "place", base, excludeID)
}

// uniqueSlugIn is the shared slug de-duplicator for any table with (id, slug).
// table and fallback are code-supplied constants, never user input, so the
// interpolation below cannot be influenced by a request.
func (s *Store) uniqueSlugIn(table, fallback, base, excludeID string) (string, error) {
	if base == "" {
		base = fallback
	}
	candidate := base
	for i := 2; ; i++ {
		var found string
		err := s.conn.QueryRow(
			`SELECT id FROM `+table+` WHERE slug = ? LIMIT 1`, candidate,
		).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && found == excludeID) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("check slug: %w", err)
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
		if i > 1000 {
			// Pathological: a thousand places named the same thing. Fall back to
			// a UUID suffix, which is collision-free by construction.
			return fmt.Sprintf("%s-%s", base, uuid.NewString()[:8]), nil
		}
	}
}

// ---------- reads ----------

// Get returns one place by id.
func (s *Store) Get(id string) (Place, error) {
	row := s.conn.QueryRow(`SELECT `+placeColumns+` FROM places WHERE id = ?`, id)
	p, err := scanPlace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Place{}, ErrNotFound
	}
	if err != nil {
		return Place{}, fmt.Errorf("get place: %w", err)
	}
	return p, nil
}

// GetBySlug returns one place by its slug, so wiki-style URLs resolve without a
// UUID lookup first.
func (s *Store) GetBySlug(slug string) (Place, error) {
	row := s.conn.QueryRow(`SELECT `+placeColumns+` FROM places WHERE slug = ?`, slug)
	p, err := scanPlace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Place{}, ErrNotFound
	}
	if err != nil {
		return Place{}, fmt.Errorf("get place by slug: %w", err)
	}
	return p, nil
}

// buildFilter renders the shared WHERE clause for List and Count so the total
// always matches the rows actually returned.
func buildFilter(opts ListOptions) (string, []any) {
	var where []string
	var args []any

	for _, raw := range opts.Tags {
		if tag := normalizeTag(raw); tag != "" {
			// Each tag gets its own EXISTS clause; chaining them with AND means a
			// place must carry every selected tag (logical AND, not OR).
			where = append(where, `EXISTS (SELECT 1 FROM json_each(places.tags) WHERE lower(json_each.value) = ?)`)
			args = append(args, tag)
		}
	}
	if city := strings.TrimSpace(opts.City); city != "" {
		where = append(where, `lower(city) = lower(?)`)
		args = append(args, city)
	}
	if q := strings.TrimSpace(opts.Q); q != "" {
		pattern := "%" + likeEscape(q) + "%"
		where = append(where, `(name LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\' OR neighborhood LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern)
	}
	if opts.SWLat != nil && opts.SWLng != nil && opts.NELat != nil && opts.NELng != nil {
		where = append(where, `lat IS NOT NULL AND lng IS NOT NULL AND lat BETWEEN ? AND ? AND lng BETWEEN ? AND ?`)
		args = append(args, *opts.SWLat, *opts.NELat, *opts.SWLng, *opts.NELng)
	}

	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// List returns a page of places newest-first, plus the total number of matches
// across all pages.
func (s *Store) List(opts ListOptions) ([]Place, int, error) {
	opts.normalize()
	filter, args := buildFilter(opts)

	var total int
	if err := s.conn.QueryRow(`SELECT COUNT(*) FROM places`+filter, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count places: %w", err)
	}

	offset := (opts.Page - 1) * opts.PerPage
	// id is the tiebreaker so two places created in the same second keep a
	// stable order across pages instead of shuffling between requests.
	query := `SELECT ` + placeColumns + ` FROM places` + filter +
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := s.conn.Query(query, append(append([]any{}, args...), opts.PerPage, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list places: %w", err)
	}
	defer rows.Close()

	places := []Place{}
	for rows.Next() {
		p, err := scanPlace(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan place: %w", err)
		}
		places = append(places, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate places: %w", err)
	}
	return places, total, nil
}

// AllTags returns every distinct tag with its place count, ordered by count
// desc then tag asc.
func (s *Store) AllTags() ([]TagCount, error) {
	rows, err := s.conn.Query(`
		SELECT lower(json_each.value) AS tag, COUNT(*) AS n
		FROM places, json_each(places.tags)
		GROUP BY tag
		ORDER BY n DESC, tag ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	out := []TagCount{}
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Tag, &tc.Count); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, tc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}
	return out, nil
}

// Count returns the total number of places, used by the health check.
func (s *Store) Count() (int, error) {
	var n int
	if err := s.conn.QueryRow(`SELECT COUNT(*) FROM places`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count places: %w", err)
	}
	return n, nil
}

// ---------- writes ----------

// Create inserts a new place. Name is required; slug is derived from the name
// when not supplied and de-duplicated either way.
func (s *Store) Create(in PlaceInput) (Place, error) {
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" {
		return Place{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if len(name) > maxNameLen {
		return Place{}, fmt.Errorf("%w: name exceeds %d bytes", ErrInvalid, maxNameLen)
	}

	p := Place{
		ID:        uuid.NewString(),
		Name:      name,
		City:      defaultCity,
		Tags:      []string{},
		CreatedAt: nowISO(),
	}
	p.UpdatedAt = p.CreatedAt

	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.SourceURL != nil {
		p.SourceURL = strings.TrimSpace(*in.SourceURL)
	}
	if in.City != nil && strings.TrimSpace(*in.City) != "" {
		p.City = strings.TrimSpace(*in.City)
	}
	if in.Neighborhood != nil {
		p.Neighborhood = strings.TrimSpace(*in.Neighborhood)
	}
	if in.Visited != nil {
		p.Visited = *in.Visited
	}
	p.Lat, p.Lng = in.Lat, in.Lng

	if in.Tags != nil {
		tags, err := normalizeTags(*in.Tags)
		if err != nil {
			return Place{}, err
		}
		p.Tags = tags
	}

	if err := validatePlace(&p); err != nil {
		return Place{}, err
	}

	base := ""
	if in.Slug != nil {
		base = slugify(*in.Slug)
	}
	if base == "" {
		base = slugify(p.Name)
	}
	slug, err := s.uniqueSlug(base, p.ID)
	if err != nil {
		return Place{}, err
	}
	p.Slug = slug

	_, err = s.conn.Exec(`
		INSERT INTO places (`+placeColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Slug, p.Description, p.SourceURL,
		p.Lat, p.Lng, p.City, p.Neighborhood, encodeTags(p.Tags),
		p.Visited, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return Place{}, fmt.Errorf("insert place: %w", err)
	}
	return p, nil
}

// Update applies a partial update to an existing place. Omitted (nil) fields
// are left untouched; created_at is never changed and updated_at always is.
func (s *Store) Update(id string, in PlaceInput) (Place, error) {
	p, err := s.Get(id)
	if err != nil {
		return Place{}, err
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return Place{}, fmt.Errorf("%w: name cannot be empty", ErrInvalid)
		}
		if len(name) > maxNameLen {
			return Place{}, fmt.Errorf("%w: name exceeds %d bytes", ErrInvalid, maxNameLen)
		}
		p.Name = name
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.SourceURL != nil {
		p.SourceURL = strings.TrimSpace(*in.SourceURL)
	}
	if in.City != nil {
		city := strings.TrimSpace(*in.City)
		if city == "" {
			city = defaultCity
		}
		p.City = city
	}
	if in.Neighborhood != nil {
		p.Neighborhood = strings.TrimSpace(*in.Neighborhood)
	}
	if in.Visited != nil {
		p.Visited = *in.Visited
	}
	// Coordinates move as a pair, so only overwrite when the request carried at
	// least one of them — otherwise a PUT that omits both would wipe the pin.
	if in.Lat != nil || in.Lng != nil {
		p.Lat, p.Lng = in.Lat, in.Lng
	}
	if in.Tags != nil {
		tags, err := normalizeTags(*in.Tags)
		if err != nil {
			return Place{}, err
		}
		p.Tags = tags
	}
	if in.Slug != nil {
		base := slugify(*in.Slug)
		if base == "" {
			base = slugify(p.Name)
		}
		slug, err := s.uniqueSlug(base, p.ID)
		if err != nil {
			return Place{}, err
		}
		p.Slug = slug
	}

	if err := validatePlace(&p); err != nil {
		return Place{}, err
	}
	p.UpdatedAt = nowISO()

	_, err = s.conn.Exec(`
		UPDATE places SET
			name = ?, slug = ?, description = ?, source_url = ?,
			lat = ?, lng = ?, city = ?, neighborhood = ?, tags = ?,
			visited = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, p.Slug, p.Description, p.SourceURL,
		p.Lat, p.Lng, p.City, p.Neighborhood, encodeTags(p.Tags),
		p.Visited, p.UpdatedAt, p.ID,
	)
	if err != nil {
		return Place{}, fmt.Errorf("update place: %w", err)
	}
	return p, nil
}

// Delete removes a place. Returns ErrNotFound if the id was already gone, so a
// double-delete reads as 404 rather than a silent success.
func (s *Store) Delete(id string) error {
	res, err := s.conn.Exec(`DELETE FROM places WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete place: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete place: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// validatePlace enforces the field limits shared by Create and Update.
func validatePlace(p *Place) error {
	if len(p.Description) > maxDescriptionLen {
		return fmt.Errorf("%w: description exceeds %d bytes", ErrInvalid, maxDescriptionLen)
	}
	if len(p.SourceURL) > maxSourceURLLen {
		return fmt.Errorf("%w: source_url exceeds %d bytes", ErrInvalid, maxSourceURLLen)
	}
	return validateCoords(p.Lat, p.Lng)
}

// ---------- seed ----------

func ptrStr(s string) *string       { return &s }
func ptrF64(f float64) *float64     { return &f }
func ptrTags(t ...string) *[]string { return &t }

// seedPlaces is the starter set written into an empty database so a fresh
// checkout has markers on the map and rows in the list immediately.
var seedPlaces = []PlaceInput{
	{
		Name:         ptrStr("Katz's Delicatessen"),
		Neighborhood: ptrStr("Lower East Side"),
		Description: ptrStr("Cash-and-ticket pastrami institution open since 1888. " +
			"Grab a ticket at the door, order at the counter (tip the cutter), and *do not* lose the ticket — " +
			"there is a surcharge and a lecture if you do.\n\nGo at an off hour; the line at 1pm Saturday is real."),
		SourceURL: ptrStr("https://www.katzsdelicatessen.com/"),
		Lat:       ptrF64(40.7223), Lng: ptrF64(-73.9874),
		Tags:    ptrTags("food", "deli", "classic", "manhattan"),
		Visited: func() *bool { b := true; return &b }(),
	},
	{
		Name:         ptrStr("The Cloisters"),
		Neighborhood: ptrStr("Washington Heights"),
		Description: ptrStr("The Met's medieval branch, assembled from pieces of actual French abbeys and " +
			"perched above the Hudson in Fort Tryon Park.\n\nThe unicorn tapestries are the headline, but the " +
			"herb garden and the view north over the river are the reason to stay."),
		SourceURL: ptrStr("https://www.metmuseum.org/visit/plan-your-visit/met-cloisters"),
		Lat:       ptrF64(40.8649), Lng: ptrF64(-73.9319),
		Tags: ptrTags("museum", "art", "outdoors", "manhattan"),
	},
	{
		Name:         ptrStr("Roberta's"),
		Neighborhood: ptrStr("Bushwick"),
		Description: ptrStr("The pizza place that rearranged Bushwick around itself. Wood-fired, " +
			"chaotic backyard, radio station in a shipping container out back.\n\nOrder the Bee Sting."),
		SourceURL: ptrStr("https://www.robertaspizza.com/"),
		Lat:       ptrF64(40.7050), Lng: ptrF64(-73.9337),
		Tags: ptrTags("food", "pizza", "brooklyn"),
	},
	{
		Name:         ptrStr("Green-Wood Cemetery"),
		Neighborhood: ptrStr("Greenwood Heights"),
		Description: ptrStr("478 acres of Victorian garden cemetery on Brooklyn's highest natural point. " +
			"Predates Central Park as a public green space and it shows — this was a *destination* in 1850.\n\n" +
			"Monk parakeets nest in the Gothic entrance arch. Battle Hill has a skyline view worth the climb."),
		SourceURL: ptrStr("https://www.green-wood.com/"),
		Lat:       ptrF64(40.6579), Lng: ptrF64(-73.9940),
		Tags: ptrTags("outdoors", "history", "walk", "brooklyn"),
	},
	{
		Name:         ptrStr("McSorley's Old Ale House"),
		Neighborhood: ptrStr("East Village"),
		Description: ptrStr("Sawdust on the floor, two choices (light or dark), served two mugs at a time. " +
			"Open since 1854 and did not admit women until 1970, under court order.\n\n" +
			"The wishbones over the bar were hung by WWI draftees who never came back to collect them."),
		SourceURL: ptrStr("https://www.mcsorleysoldalehouse.nyc/"),
		Lat:       ptrF64(40.7286), Lng: ptrF64(-73.9897),
		Tags: ptrTags("bar", "classic", "history", "manhattan"),
	},
}

// SeedIfEmpty writes the starter places when the table has no rows. It is a
// no-op on a database that already holds anything, so restarting the server
// never duplicates or resurrects deleted seed entries.
func (s *Store) SeedIfEmpty() (int, error) {
	n, err := s.Count()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	written := 0
	for _, in := range seedPlaces {
		if _, err := s.Create(in); err != nil {
			return written, fmt.Errorf("seed place: %w", err)
		}
		written++
	}
	return written, nil
}

// ---------- trips ----------

// ErrTripNotFound is returned when a trip id or slug is unknown, or when a stop
// that should belong to a trip does not. The API maps it to a 404.
var ErrTripNotFound = errors.New("trip not found")

// ErrInvalidTrip is the trip-side counterpart to ErrInvalid: a write that
// violates a field constraint. The API maps it to a 400 and surfaces the
// wrapped message, so wrap it with something a human can act on.
var ErrInvalidTrip = errors.New("invalid trip")

const (
	maxTripTypeLen  = 64
	maxRouteLen     = 200
	maxStopNotesLen = 20_000
	maxTagsPerTrip  = 30
	maxDurationDays = 3650 // ten years; a trip longer than that is a data-entry slip

	defaultTripType = "trip"
	// unroutedLabel is the synthetic group holding stops with no route label, so
	// the detail response never has a nameless segment.
	unroutedLabel = "Stops"
)

// Trip is a named collection of places. It is also the list-view shape:
// PlaceCount is computed per query rather than stored, so it cannot drift out
// of sync with trip_places.
type Trip struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Description  string   `json:"description"`
	TripType     string   `json:"trip_type"`
	Tags         []string `json:"tags"`
	DurationDays *int     `json:"duration_days"`
	SourceURL    string   `json:"source_url"`
	PlaceCount   int      `json:"place_count"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// TripStop is a place as it appears inside a trip: the full place record plus
// the three columns that only mean something in this trip's context. Place is
// embedded without a json tag so its fields inline into the stop object rather
// than nesting under "place".
type TripStop struct {
	Place
	Route      string `json:"route"`
	RouteOrder int    `json:"route_order"`
	StopNotes  string `json:"stop_notes"`
}

// TripRoute is one segment of a trip — a labelled run of stops walked in order.
type TripRoute struct {
	Label string     `json:"label"`
	Stops []TripStop `json:"stops"`
}

// TripDetail is the GET /api/trips/{id} shape: the trip plus its stops already
// grouped into route segments, so the frontend doesn't re-derive the grouping.
type TripDetail struct {
	Trip
	Routes []TripRoute `json:"routes"`
}

// TripInput is the writable shape accepted by POST/PUT, following PlaceInput's
// all-pointer convention so a PUT can distinguish "omitted" from "cleared".
// DurationDays is the one exception in spirit: a non-positive value clears the
// duration, since "a trip lasting zero days" is not a thing anyone means.
type TripInput struct {
	Name         *string   `json:"name"`
	Slug         *string   `json:"slug"`
	Description  *string   `json:"description"`
	TripType     *string   `json:"trip_type"`
	Tags         *[]string `json:"tags"`
	DurationDays *int      `json:"duration_days"`
	SourceURL    *string   `json:"source_url"`
}

// TripPlaceInput is the body of POST /api/trips/{id}/places. Only PlaceID is
// required; the rest describe where the stop sits in the itinerary.
type TripPlaceInput struct {
	PlaceID    *string `json:"place_id"`
	Route      *string `json:"route"`
	RouteOrder *int    `json:"route_order"`
	StopNotes  *string `json:"stop_notes"`
}

const tripColumns = `id, name, slug, description, trip_type, tags, duration_days, source_url, created_at, updated_at`

// tripSelect reads a trip plus its live stop count. The correlated subquery is
// cheap here — trip_places is keyed on (trip_id, place_id), so counting a
// trip's stops is an index range scan.
const tripSelect = `
	SELECT t.id, t.name, t.slug, t.description, t.trip_type, t.tags,
	       t.duration_days, t.source_url, t.created_at, t.updated_at,
	       (SELECT COUNT(*) FROM trip_places tp WHERE tp.trip_id = t.id) AS place_count
	FROM trips t`

func scanTrip(sc interface{ Scan(...any) error }) (Trip, error) {
	var t Trip
	var description, tripType, rawTags, sourceURL sql.NullString
	var duration sql.NullInt64
	err := sc.Scan(
		&t.ID, &t.Name, &t.Slug, &description, &tripType, &rawTags,
		&duration, &sourceURL, &t.CreatedAt, &t.UpdatedAt, &t.PlaceCount,
	)
	if err != nil {
		return Trip{}, err
	}
	// Every one of these columns is nullable in the schema, so normalize NULL to
	// the zero value rather than leaking a null into JSON the frontend then has
	// to guard on.
	t.Description = description.String
	t.TripType = tripType.String
	if t.TripType == "" {
		t.TripType = defaultTripType
	}
	t.SourceURL = sourceURL.String
	t.Tags = decodeTags(rawTags.String)
	if duration.Valid {
		d := int(duration.Int64)
		t.DurationDays = &d
	}
	return t, nil
}

// uniqueTripSlug is uniqueSlug's trip-table twin.
func (s *Store) uniqueTripSlug(base, excludeID string) (string, error) {
	return s.uniqueSlugIn("trips", "trip", base, excludeID)
}

// ---------- trip reads ----------

// ListTrips returns every trip newest-first with its stop count. Trips are a
// handful of curated itineraries rather than a growing feed, so this is
// deliberately unpaginated.
func (s *Store) ListTrips() ([]Trip, error) {
	rows, err := s.conn.Query(tripSelect + ` ORDER BY t.created_at DESC, t.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list trips: %w", err)
	}
	defer rows.Close()

	trips := []Trip{}
	for rows.Next() {
		t, err := scanTrip(rows)
		if err != nil {
			return nil, fmt.Errorf("scan trip: %w", err)
		}
		trips = append(trips, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trips: %w", err)
	}
	return trips, nil
}

// GetTrip returns one trip's metadata by id, without its stops.
func (s *Store) GetTrip(id string) (Trip, error) {
	row := s.conn.QueryRow(tripSelect+` WHERE t.id = ?`, id)
	t, err := scanTrip(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Trip{}, ErrTripNotFound
	}
	if err != nil {
		return Trip{}, fmt.Errorf("get trip: %w", err)
	}
	return t, nil
}

// GetTripBySlug returns one trip's metadata by slug.
func (s *Store) GetTripBySlug(slug string) (Trip, error) {
	row := s.conn.QueryRow(tripSelect+` WHERE t.slug = ?`, slug)
	t, err := scanTrip(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Trip{}, ErrTripNotFound
	}
	if err != nil {
		return Trip{}, fmt.Errorf("get trip by slug: %w", err)
	}
	return t, nil
}

// tripStopsQuery joins trip_places onto places and imposes the itinerary order:
// labelled segments first in label order, the synthetic unlabelled group last,
// and within a segment by route_order then name. Ordering by label (rather than
// by, say, the minimum route_order in each group) is what makes route_order
// safe to restart at 0 in every segment, which is how anyone numbering
// "Route 1 / Route 2" stops actually writes them.
const tripStopsQuery = `
	SELECT p.id, p.name, p.slug, p.description, p.source_url, p.lat, p.lng,
	       p.city, p.neighborhood, p.tags, p.visited, p.created_at, p.updated_at,
	       tp.route, tp.route_order, tp.stop_notes
	FROM trip_places tp
	JOIN places p ON p.id = tp.place_id
	WHERE tp.trip_id = ?
	ORDER BY CASE WHEN COALESCE(TRIM(tp.route), '') = '' THEN 1 ELSE 0 END ASC,
	         tp.route ASC, tp.route_order ASC, p.name ASC`

func scanTripStop(rows *sql.Rows) (TripStop, error) {
	var st TripStop
	var rawTags string
	var lat, lng sql.NullFloat64
	var route, notes sql.NullString
	var order sql.NullInt64
	err := rows.Scan(
		&st.ID, &st.Name, &st.Slug, &st.Description, &st.SourceURL,
		&lat, &lng, &st.City, &st.Neighborhood, &rawTags,
		&st.Visited, &st.CreatedAt, &st.UpdatedAt,
		&route, &order, &notes,
	)
	if err != nil {
		return TripStop{}, err
	}
	if lat.Valid {
		st.Lat = &lat.Float64
	}
	if lng.Valid {
		st.Lng = &lng.Float64
	}
	st.Tags = decodeTags(rawTags)
	st.Route = strings.TrimSpace(route.String)
	st.RouteOrder = int(order.Int64)
	st.StopNotes = notes.String
	return st, nil
}

// TripStops returns a trip's stops in itinerary order, flat.
func (s *Store) TripStops(tripID string) ([]TripStop, error) {
	rows, err := s.conn.Query(tripStopsQuery, tripID)
	if err != nil {
		return nil, fmt.Errorf("list trip stops: %w", err)
	}
	defer rows.Close()

	stops := []TripStop{}
	for rows.Next() {
		st, err := scanTripStop(rows)
		if err != nil {
			return nil, fmt.Errorf("scan trip stop: %w", err)
		}
		stops = append(stops, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trip stops: %w", err)
	}
	return stops, nil
}

// groupByRoute folds ordered stops into route segments, preserving the order
// the query imposed. Unlabelled stops collect into the synthetic "Stops" group,
// which the query has already sorted last.
func groupByRoute(stops []TripStop) []TripRoute {
	routes := []TripRoute{}
	index := make(map[string]int, len(stops))
	for _, st := range stops {
		label := st.Route
		if label == "" {
			label = unroutedLabel
		}
		i, ok := index[label]
		if !ok {
			i = len(routes)
			index[label] = i
			routes = append(routes, TripRoute{Label: label, Stops: []TripStop{}})
		}
		routes[i].Stops = append(routes[i].Stops, st)
	}
	return routes
}

// TripDetailByID returns a trip with its stops grouped into route segments.
func (s *Store) TripDetailByID(id string) (TripDetail, error) {
	t, err := s.GetTrip(id)
	if err != nil {
		return TripDetail{}, err
	}
	return s.detailFor(t)
}

// TripDetailBySlug is TripDetailByID addressed by slug, for wiki-style URLs.
func (s *Store) TripDetailBySlug(slug string) (TripDetail, error) {
	t, err := s.GetTripBySlug(slug)
	if err != nil {
		return TripDetail{}, err
	}
	return s.detailFor(t)
}

func (s *Store) detailFor(t Trip) (TripDetail, error) {
	stops, err := s.TripStops(t.ID)
	if err != nil {
		return TripDetail{}, err
	}
	return TripDetail{Trip: t, Routes: groupByRoute(stops)}, nil
}

// CountTrips returns the total number of trips, used by the health check and by
// the seeder to decide whether there is anything to seed.
func (s *Store) CountTrips() (int, error) {
	var n int
	if err := s.conn.QueryRow(`SELECT COUNT(*) FROM trips`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count trips: %w", err)
	}
	return n, nil
}

// ---------- trip writes ----------

// applyTripInput folds a partial input onto a trip and validates the result.
// Shared by CreateTrip and UpdateTrip so the two can never disagree about what
// a legal trip looks like.
func applyTripInput(t *Trip, in TripInput) error {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return fmt.Errorf("%w: name cannot be empty", ErrInvalidTrip)
		}
		if len(name) > maxNameLen {
			return fmt.Errorf("%w: name exceeds %d bytes", ErrInvalidTrip, maxNameLen)
		}
		t.Name = name
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	if in.TripType != nil {
		tt := strings.ToLower(strings.TrimSpace(*in.TripType))
		if tt == "" {
			tt = defaultTripType
		}
		if len(tt) > maxTripTypeLen {
			return fmt.Errorf("%w: trip_type exceeds %d bytes", ErrInvalidTrip, maxTripTypeLen)
		}
		t.TripType = tt
	}
	if in.SourceURL != nil {
		t.SourceURL = strings.TrimSpace(*in.SourceURL)
	}
	if in.Tags != nil {
		tags, err := normalizeTags(*in.Tags)
		if err != nil {
			// normalizeTags speaks in ErrInvalid; re-wrap so a trip write reports
			// a trip problem.
			return fmt.Errorf("%w: %s", ErrInvalidTrip, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
		}
		if len(tags) > maxTagsPerTrip {
			return fmt.Errorf("%w: %d tags exceeds the limit of %d", ErrInvalidTrip, len(tags), maxTagsPerTrip)
		}
		t.Tags = tags
	}
	if in.DurationDays != nil {
		// Non-positive clears the duration; the column is nullable precisely so
		// "however long you like" is representable.
		if *in.DurationDays <= 0 {
			t.DurationDays = nil
		} else if *in.DurationDays > maxDurationDays {
			return fmt.Errorf("%w: duration_days %d exceeds %d", ErrInvalidTrip, *in.DurationDays, maxDurationDays)
		} else {
			d := *in.DurationDays
			t.DurationDays = &d
		}
	}

	if len(t.Description) > maxDescriptionLen {
		return fmt.Errorf("%w: description exceeds %d bytes", ErrInvalidTrip, maxDescriptionLen)
	}
	if len(t.SourceURL) > maxSourceURLLen {
		return fmt.Errorf("%w: source_url exceeds %d bytes", ErrInvalidTrip, maxSourceURLLen)
	}
	return nil
}

// CreateTrip inserts a new trip. Name is required; slug is derived from the
// name when not supplied and de-duplicated either way.
func (s *Store) CreateTrip(in TripInput) (Trip, error) {
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return Trip{}, fmt.Errorf("%w: name is required", ErrInvalidTrip)
	}

	t := Trip{
		ID:        uuid.NewString(),
		TripType:  defaultTripType,
		Tags:      []string{},
		CreatedAt: nowISO(),
	}
	t.UpdatedAt = t.CreatedAt

	if err := applyTripInput(&t, in); err != nil {
		return Trip{}, err
	}

	base := ""
	if in.Slug != nil {
		base = slugify(*in.Slug)
	}
	if base == "" {
		base = slugify(t.Name)
	}
	slug, err := s.uniqueTripSlug(base, t.ID)
	if err != nil {
		return Trip{}, err
	}
	t.Slug = slug

	_, err = s.conn.Exec(`
		INSERT INTO trips (`+tripColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Slug, t.Description, t.TripType, encodeTags(t.Tags),
		t.DurationDays, t.SourceURL, t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		return Trip{}, fmt.Errorf("insert trip: %w", err)
	}
	return t, nil
}

// UpdateTrip applies a partial update to a trip's metadata. Its stops are not
// touched — those move through the /places sub-resource.
func (s *Store) UpdateTrip(id string, in TripInput) (Trip, error) {
	t, err := s.GetTrip(id)
	if err != nil {
		return Trip{}, err
	}
	if err := applyTripInput(&t, in); err != nil {
		return Trip{}, err
	}
	if in.Slug != nil {
		base := slugify(*in.Slug)
		if base == "" {
			base = slugify(t.Name)
		}
		slug, err := s.uniqueTripSlug(base, t.ID)
		if err != nil {
			return Trip{}, err
		}
		t.Slug = slug
	}
	t.UpdatedAt = nowISO()

	_, err = s.conn.Exec(`
		UPDATE trips SET
			name = ?, slug = ?, description = ?, trip_type = ?, tags = ?,
			duration_days = ?, source_url = ?, updated_at = ?
		WHERE id = ?`,
		t.Name, t.Slug, t.Description, t.TripType, encodeTags(t.Tags),
		t.DurationDays, t.SourceURL, t.UpdatedAt, t.ID,
	)
	if err != nil {
		return Trip{}, fmt.Errorf("update trip: %w", err)
	}
	return t, nil
}

// DeleteTrip removes a trip. Its trip_places rows go with it via ON DELETE
// CASCADE (foreign_keys is ON in the DSN); the places themselves are untouched,
// since a place outlives any itinerary that references it.
func (s *Store) DeleteTrip(id string) error {
	res, err := s.conn.Exec(`DELETE FROM trips WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete trip: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete trip: %w", err)
	}
	if n == 0 {
		return ErrTripNotFound
	}
	return nil
}

// AddTripPlace adds a place to a trip, or updates the stop's route, ordering
// and notes if it is already on the itinerary. Re-adding is an update rather
// than a conflict because "add this place to route 2 instead" is the common
// correction, and a 409 would just make the client delete-then-add.
func (s *Store) AddTripPlace(tripID string, in TripPlaceInput) (TripStop, error) {
	if _, err := s.GetTrip(tripID); err != nil {
		return TripStop{}, err
	}
	if in.PlaceID == nil || strings.TrimSpace(*in.PlaceID) == "" {
		return TripStop{}, fmt.Errorf("%w: place_id is required", ErrInvalidTrip)
	}
	placeID := strings.TrimSpace(*in.PlaceID)
	// Checked explicitly so an unknown place reads as a 404 naming the place,
	// rather than a bare foreign-key violation from SQLite.
	place, err := s.Get(placeID)
	if err != nil {
		return TripStop{}, err
	}

	stop := TripStop{Place: place}
	if in.Route != nil {
		route := strings.TrimSpace(*in.Route)
		if len(route) > maxRouteLen {
			return TripStop{}, fmt.Errorf("%w: route exceeds %d bytes", ErrInvalidTrip, maxRouteLen)
		}
		stop.Route = route
	}
	if in.RouteOrder != nil {
		stop.RouteOrder = *in.RouteOrder
	}
	if in.StopNotes != nil {
		if len(*in.StopNotes) > maxStopNotesLen {
			return TripStop{}, fmt.Errorf("%w: stop_notes exceeds %d bytes", ErrInvalidTrip, maxStopNotesLen)
		}
		stop.StopNotes = *in.StopNotes
	}

	_, err = s.conn.Exec(`
		INSERT INTO trip_places (trip_id, place_id, route, route_order, stop_notes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (trip_id, place_id) DO UPDATE SET
			route = excluded.route,
			route_order = excluded.route_order,
			stop_notes = excluded.stop_notes`,
		tripID, placeID, stop.Route, stop.RouteOrder, stop.StopNotes,
	)
	if err != nil {
		return TripStop{}, fmt.Errorf("add trip place: %w", err)
	}
	s.touchTrip(tripID)
	return stop, nil
}

// RemoveTripPlace takes a place off a trip. The place itself survives.
func (s *Store) RemoveTripPlace(tripID, placeID string) error {
	res, err := s.conn.Exec(
		`DELETE FROM trip_places WHERE trip_id = ? AND place_id = ?`, tripID, placeID)
	if err != nil {
		return fmt.Errorf("remove trip place: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove trip place: %w", err)
	}
	if n == 0 {
		// Either the trip is unknown or the place was never a stop on it. Both
		// are a 404, but they are different mistakes, so say which one happened
		// rather than reporting a missing trip that is sitting right there.
		if _, err := s.GetTrip(tripID); err != nil {
			return err
		}
		return fmt.Errorf("%w: that place is not a stop on this trip", ErrTripNotFound)
	}
	s.touchTrip(tripID)
	return nil
}

// touchTrip bumps updated_at after a stop change, so a trip's timestamp
// reflects edits to its itinerary and not just to its metadata. A failure here
// is cosmetic — the stop change itself already committed — so it is logged by
// the caller's error path rather than propagated.
func (s *Store) touchTrip(tripID string) {
	_, _ = s.conn.Exec(`UPDATE trips SET updated_at = ? WHERE id = ?`, nowISO(), tripID)
}

// ---------- trip seed ----------

func ptrInt(i int) *int { return &i }

// seedStop is one place on a seeded itinerary: the place to create if it isn't
// already in the wiki, plus where it sits in the trip.
type seedStop struct {
	place      PlaceInput
	route      string
	routeOrder int
	notes      string
}

// seedTrip is a starter itinerary and its stops.
type seedTrip struct {
	trip  TripInput
	stops []seedStop
}

// designShopTags is the shared tag set for the furniture and design dealers on
// the crawl — spelled once so the whole segment stays filterable as a unit.
func designShopTags() *[]string {
	return ptrTags("design", "furniture", "shopping", "vintage")
}

// seedTrips is the starter itinerary set. Each stop's place is created only if
// the wiki doesn't already hold one under the same slug, so seeding a database
// that already has places adds the trip without duplicating entries.
var seedTrips = []seedTrip{
	{
		trip: TripInput{
			Name:     ptrStr("NYC Design Shopping Crawl"),
			Slug:     ptrStr("nyc-design-shopping-crawl"),
			TripType: ptrStr("crawl"),
			Tags:     ptrTags("design", "shopping"),
			Description: ptrStr("Ten furniture and design dealers across Manhattan, Brooklyn and Queens, " +
				"split into three walkable runs. Two days is comfortable: Manhattan on day one, the " +
				"outer-borough warehouses on day two.\n\n" +
				"Most of these are galleries and showrooms rather than shops — smaller rooms keep " +
				"appointment-ish hours and everything thins out on Mondays, so check before you go. " +
				"The Brooklyn and Queens stops are the ones with actual square footage, and the ones " +
				"most worth the trip if you are buying rather than looking."),
			DurationDays: ptrInt(2),
		},
		stops: []seedStop{
			// Route 1 — Manhattan, south to north: Tribeca, then SoHo, NoHo and
			// the East Village. All walkable end to end.
			{
				route: "Route 1 — Downtown Manhattan", routeOrder: 0,
				notes: "Start here. A designer-run cooperative showroom, so the mix is independent " +
					"American studios rather than one house style.",
				place: PlaceInput{
					Name:         ptrStr("Colony"),
					Neighborhood: ptrStr("Tribeca"),
					Description: ptrStr("Cooperative showroom at 196 W Broadway representing independent " +
						"American furniture, lighting and textile designers under one roof.\n\n" +
						"Good first stop on a design crawl: it is the widest survey of who is actually " +
						"making things in the US right now, which makes everything after it easier to place."),
					Lat: ptrF64(40.7196), Lng: ptrF64(-74.0082),
					Tags: designShopTags(),
				},
			},
			{
				route: "Route 1 — Downtown Manhattan", routeOrder: 1,
				notes: "Two minutes from Colony. Brazilian modernism — the rosewood-and-leather end of " +
					"mid-century, and a different vocabulary from everything else on this route.",
				place: PlaceInput{
					Name:         ptrStr("Espasso"),
					Neighborhood: ptrStr("Tribeca"),
					Description: ptrStr("Brazilian modernist furniture at 38 N Moore St — both vintage pieces " +
						"and authorized reproductions from the mid-century Brazilian canon.\n\n" +
						"Worth going in even with no intention of buying; it is the easiest place in the city " +
						"to see this material in person."),
					Lat: ptrF64(40.7196), Lng: ptrF64(-74.0082),
					Tags: designShopTags(),
				},
			},
			{
				route: "Route 1 — Downtown Manhattan", routeOrder: 2,
				notes: "Ten minutes north-east into SoHo. Collectible/contemporary rather than vintage — " +
					"the gallery end of the spectrum.",
				place: PlaceInput{
					Name:         ptrStr("Raisonné"),
					Neighborhood: ptrStr("SoHo"),
					Description: ptrStr("Collectible design gallery at 16 Crosby St, showing contemporary " +
						"and limited-edition furniture and objects.\n\n" +
						"Small room, curated tightly, hours worth checking before you walk over."),
					Lat: ptrF64(40.7223), Lng: ptrF64(-74.0003),
					Tags: designShopTags(),
				},
			},
			{
				route: "Route 1 — Downtown Manhattan", routeOrder: 3,
				notes: "Bond St is a short walk up from Crosby. Vintage Scandinavian and Japanese-influenced " +
					"modern; the density per square foot here is high.",
				place: PlaceInput{
					Name:         ptrStr("ModernLink"),
					Neighborhood: ptrStr("NoHo"),
					Description: ptrStr("Vintage modern furniture at 35 Bond St, leaning Danish and Japanese, " +
						"alongside custom pieces built in the same idiom.\n\n" +
						"One of the longer-running dealers in the neighborhood, on one of the better blocks " +
						"in it."),
					Lat: ptrF64(40.7263), Lng: ptrF64(-73.9942),
					Tags: designShopTags(),
				},
			},
			{
				route: "Route 1 — Downtown Manhattan", routeOrder: 4,
				notes: "End of the Manhattan walk. Objects and tableware rather than furniture — the right " +
					"scale of thing to actually carry home.",
				place: PlaceInput{
					Name:         ptrStr("Nalata Nalata"),
					Neighborhood: ptrStr("East Village"),
					Description: ptrStr("Japanese craft and homeware on Extra Place, the alley off E 1st St: " +
						"ceramics, tools, textiles and small furniture, mostly from makers the owners visit " +
						"directly.\n\n" +
						"The shop itself is a single quiet room and is worth the detour for the merchandising " +
						"alone."),
					Lat: ptrF64(40.7265), Lng: ptrF64(-73.9889),
					Tags: ptrTags("design", "shopping", "craft", "homeware"),
				},
			},

			// Route 2 — Brooklyn, north to south along the G: Greenpoint,
			// Williamsburg, Clinton Hill. This is the warehouse day.
			{
				route: "Route 2 — Brooklyn", routeOrder: 0,
				notes: "Start at the top of the G line. Warehouse-scale vintage — budget real time here, " +
					"it is not a ten-minute stop.",
				place: PlaceInput{
					Name:         ptrStr("Renew Finds"),
					Neighborhood: ptrStr("Greenpoint"),
					Description: ptrStr("Large vintage and mid-century furniture warehouse at 80 Oak St, " +
						"a block from the water.\n\n" +
						"Volume over curation, which is the point: this is where you find the thing rather " +
						"than where someone has already found it for you."),
					Lat: ptrF64(40.7253), Lng: ptrF64(-73.9533),
					Tags: designShopTags(),
				},
			},
			{
				route: "Route 2 — Brooklyn", routeOrder: 1,
				notes: "South into Williamsburg. Come for the furniture, stay for the design book shelves — " +
					"the out-of-print stock is genuinely rare.",
				place: PlaceInput{
					Name:         ptrStr("Open Air Modern"),
					Neighborhood: ptrStr("Williamsburg"),
					Description: ptrStr("Vintage modern furniture at 489 Lorimer St, paired with a serious " +
						"collection of rare and out-of-print design, architecture and art books.\n\n" +
						"The books are the differentiator; several dealers in this city sell the chairs, " +
						"far fewer sell the monograph about the chair."),
					Lat: ptrF64(40.7178), Lng: ptrF64(-73.9497),
					Tags: designShopTags(),
				},
			},
			{
				route: "Route 2 — Brooklyn", routeOrder: 2,
				notes: "Last Brooklyn stop, down by the Navy Yard end of Clinton Hill. Tightly edited " +
					"Scandinavian — a calm finish after two warehouses.",
				place: PlaceInput{
					Name:         ptrStr("Lanoba Design"),
					Neighborhood: ptrStr("Clinton Hill"),
					Description: ptrStr("Vintage Scandinavian and mid-century furniture at 6 Waverly Ave, " +
						"restored in-house.\n\n" +
						"Smaller and more edited than the Greenpoint and Williamsburg warehouses, and priced " +
						"accordingly."),
					Lat: ptrF64(40.6849), Lng: ptrF64(-73.9692),
					Tags: designShopTags(),
				},
			},

			// Route 3 — the 7 train run: Long Island City out to the West Side.
			// Two stops, both worth a dedicated half-day rather than a detour.
			{
				route: "Route 3 — Long Island City & the West Side", routeOrder: 0,
				notes: "Queens, five minutes from the Vernon Blvd–Jackson Ave stop. Props-warehouse energy: " +
					"vintage furniture mixed with decor and set dressing.",
				place: PlaceInput{
					Name:         ptrStr("The Somerset House"),
					Neighborhood: ptrStr("Long Island City"),
					Description: ptrStr("Vintage furniture, lighting and decor at 44-01 11th St, doubling as " +
						"a prop source for film and photo shoots.\n\n" +
						"Inventory turns over fast and skews eclectic rather than period-pure — go with an " +
						"open brief."),
					Lat: ptrF64(40.7475), Lng: ptrF64(-73.9501),
					Tags: designShopTags(),
				},
			},
			{
				route: "Route 3 — Long Island City & the West Side", routeOrder: 1,
				notes: "The 7 runs LIC straight to Hudson Yards; walk up 10th Ave from there. Appointment " +
					"advisable. Finish on the High Line, which starts a block away.",
				place: PlaceInput{
					Name:         ptrStr("Les Ateliers Courbet"),
					Neighborhood: ptrStr("Chelsea"),
					Description: ptrStr("Gallery at 134 10th Ave devoted to European ateliers and heritage " +
						"workshops — commissioned and limited-edition work from houses that have been at it " +
						"for generations.\n\n" +
						"The most gallery-like stop on the crawl, and the one most likely to want notice " +
						"before you turn up."),
					Lat: ptrF64(40.7456), Lng: ptrF64(-74.0055),
					Tags: designShopTags(),
				},
			},
		},
	},
}

// ensurePlace returns the existing place with this name's slug, or creates it.
// Matching on slug rather than name is what makes re-seeding idempotent even
// after someone has edited a place's name casing or punctuation.
func (s *Store) ensurePlace(in PlaceInput) (Place, error) {
	if in.Name == nil {
		return Place{}, fmt.Errorf("%w: seed place has no name", ErrInvalid)
	}
	slug := slugify(*in.Name)
	if slug != "" {
		existing, err := s.GetBySlug(slug)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Place{}, err
		}
	}
	return s.Create(in)
}

// SeedTripsIfEmpty writes the starter itineraries when the trips table has no
// rows, creating any of their places that the wiki doesn't already hold. Like
// SeedIfEmpty it is a no-op once anything exists, so a restart never
// resurrects a deleted trip.
func (s *Store) SeedTripsIfEmpty() (int, error) {
	n, err := s.CountTrips()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}

	written := 0
	for _, st := range seedTrips {
		trip, err := s.CreateTrip(st.trip)
		if err != nil {
			return written, fmt.Errorf("seed trip: %w", err)
		}
		for _, stop := range st.stops {
			place, err := s.ensurePlace(stop.place)
			if err != nil {
				return written, fmt.Errorf("seed trip place: %w", err)
			}
			_, err = s.AddTripPlace(trip.ID, TripPlaceInput{
				PlaceID:    &place.ID,
				Route:      ptrStr(stop.route),
				RouteOrder: ptrInt(stop.routeOrder),
				StopNotes:  ptrStr(stop.notes),
			})
			if err != nil {
				return written, fmt.Errorf("seed trip stop: %w", err)
			}
		}
		written++
	}
	return written, nil
}
