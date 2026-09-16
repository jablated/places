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
	Tag     string
	City    string
	Q       string
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

// uniqueSlug returns base, or base-2, base-3, ... if base is already taken.
// excludeID lets an update keep its own slug without colliding with itself.
func (s *Store) uniqueSlug(base, excludeID string) (string, error) {
	if base == "" {
		base = "place"
	}
	candidate := base
	for i := 2; ; i++ {
		var found string
		err := s.conn.QueryRow(
			`SELECT id FROM places WHERE slug = ? LIMIT 1`, candidate,
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

	if tag := normalizeTag(opts.Tag); tag != "" {
		// json_each expands the stored JSON array into rows, so this matches a
		// whole tag rather than a substring — a LIKE '%bar%' would also match
		// "bars" and "barbecue".
		where = append(where, `EXISTS (SELECT 1 FROM json_each(places.tags) WHERE lower(json_each.value) = ?)`)
		args = append(args, tag)
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
