#!/usr/bin/env python3
"""
Sync the Obsidian NYC places wiki into the places API.

Reads every *.md file under --wiki-root/wiki/places/, parses the YAML
frontmatter, and upserts each place into the API via POST (create) or
PUT (update). Existing records are matched by slug (derived from the
filename). The script fetches all existing places once at startup, so
it makes one bulk read + one write per place — no N+1 queries.

Files whose frontmatter fails to parse are retried once against a
sanitized copy in which unquoted values starting with a reserved YAML
indicator character (@ & * ! % ` | >) — e.g. `instagram_handle: @foo`
— are double-quoted. If the retry also fails, the file is a parse
failure: it is reported to stderr as `parse-failed:` and the run
exits 1, so the weekly cron surfaces it. Only files with junk
prefixes (`skip-`, `unknown-`) are counted as junk-skipped.

After the upsert loop the script refetches all places from the API
and reports any record whose slug matches no parsed wiki file
("orphan") to stderr, with the count in the final summary. Orphans
are report-only — the script never deletes or modifies them.

Usage:
  python3 scripts/wiki-sync.py [--wiki-root DIR] [--api-url URL] [--dry-run]

Exits 0 on success, 1 if any wiki file failed to parse or any place
failed to sync (after processing all of them).
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from typing import Any

import requests
import yaml

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

PLACES_DIR_RELATIVE = "wiki/places"
JUNK_PREFIXES = ("skip-", "unknown-")

# Wiki type → city label for places outside NYC proper.
# NYC boroughs all get city = "New York" with the borough in neighborhood.
NEAR_NYC_CITY = "New York Area"

# Frontmatter keys that hold source URLs — either spelling is accepted.
SOURCE_KEYS = ("source", "sources")


class ParseFailure(Exception):
    """A wiki file whose frontmatter failed to parse even after
    sanitization. main() reports these loudly and exits 1."""

    def __init__(self, message: str):
        super().__init__(message)
        self.reason = message


# ---------------------------------------------------------------------------
# Parsing
# ---------------------------------------------------------------------------

# Characters that cannot start an unquoted YAML scalar (reserved
# indicators). A value beginning with one of these either fails to
# parse outright (@ % ` * in flow/plain context) or changes meaning
# (& * ! | > are anchors/aliases/tags/block scalars), so such values
# are double-quoted during frontmatter sanitization.
RESERVED_INDICATORS = ("@", "&", "*", "!", "%", "`", "|", ">")

# Only sanitize simple `key: value` mappings — not nested block
# sequences/mappings, and not keys (frontmatter keys are plain scalars
# in practice).
_UNQUOTED_SCALAR_RE = re.compile(
    r"^(?P<key>[^:#\s][^:#]*):\s"
    r"(?P<value>[^'\"].*?)"
    r"(?P<comment>\s+#.*)?$"
)


def sanitize_frontmatter(fm_text: str) -> str:
    """Double-quote plain scalar values that start with a reserved
    YAML indicator character, e.g. `instagram_handle: @foo` ->
    `instagram_handle: "@foo"`. Trailing comments are preserved and
    internal quotes/backslashes are escaped. All other lines pass
    through unchanged. The result must be re-validated by the caller
    — this is a heuristic fix-up, not a general YAML rewriter.
    """
    out_lines: list[str] = []
    for line in fm_text.splitlines():
        m = _UNQUOTED_SCALAR_RE.match(line)
        if m and m.group("value").startswith(RESERVED_INDICATORS):
            value = m.group("value").replace("\\", "\\\\").replace('"', '\\"')
            comment = m.group("comment") or ""
            line = f'{m.group("key")}: "{value}"{comment}'
        out_lines.append(line)
    return "\n".join(out_lines)


def parse_wiki_file(path: Path) -> dict[str, Any]:
    """Return a dict ready for the API. Raises ParseFailure if the
    file cannot become a place: YAML frontmatter that fails to parse
    even after sanitization, no frontmatter at all, or no `name`.
    """
    text = path.read_text(encoding="utf-8")

    # Extract YAML frontmatter between the first pair of --- fences.
    fm_match = re.match(r"^---\n(.*?)\n---\n", text, re.DOTALL)
    if not fm_match:
        raise ParseFailure(f"{path.name}: no YAML frontmatter found")

    fm = None
    last_err: yaml.YAMLError | None = None
    for attempt in (fm_match.group(1), sanitize_frontmatter(fm_match.group(1))):
        try:
            fm = yaml.safe_load(attempt) or {}
            break
        except yaml.YAMLError as e:
            last_err = e

    if fm is None:
        reason = " ".join(str(last_err).split()) if last_err else "unknown YAML error"
        raise ParseFailure(
            f"{path.name}: YAML frontmatter failed to parse even after "
            f"sanitization: {reason}"
        )

    name = (fm.get("name") or "").strip()
    if not name:
        raise ParseFailure(f"{path.name}: frontmatter has no 'name' field")

    # Body after the closing --- fence.
    body = text[fm_match.end():]
    description = _extract_description(body)

    # Coordinates — treat "null" / "" / 0.0 as absent.
    lat = _nullable_float(fm.get("lat"))
    lng = _nullable_float(fm.get("lng"))

    # Tags — always a list in the wiki, but guard against scalar.
    raw_tags = fm.get("tags") or []
    if isinstance(raw_tags, str):
        raw_tags = [t.strip() for t in raw_tags.split(",") if t.strip()]
    tags = [str(t).strip() for t in raw_tags if str(t).strip()]

    # Source URL — pick the first of whatever spelling is present.
    source_url = _extract_source_url(fm)

    # City: non-NYC places get a distinct label; everything else is "New York".
    wiki_type = (fm.get("type") or "").strip()
    borough = _canonical_borough(fm.get("borough"))
    if wiki_type in ("near_nyc", "trip") and not borough:
        city = NEAR_NYC_CITY
    else:
        city = "New York"

    neighborhood = (fm.get("neighborhood") or "").strip() or None

    # Visited — coerce whatever the wiki has to bool.
    visited_raw = fm.get("visited")
    visited = bool(visited_raw) if visited_raw is not None else False

    return {
        "name": name,
        "slug": path.stem,          # filename without .md is the canonical slug
        "description": description,
        "source_url": source_url or "",
        "lat": lat,
        "lng": lng,
        "city": city,
        "neighborhood": neighborhood or "",
        "tags": tags,
        "visited": visited,
    }


def _extract_description(body: str) -> str:
    """
    Pull the prose paragraph(s) before the first ## heading,
    then append the ## Notes bullets as plain text.
    """
    lines = body.splitlines()
    prose_lines: list[str] = []
    notes_lines: list[str] = []
    in_notes = False

    for line in lines:
        stripped = line.strip()
        if stripped.startswith("## Notes"):
            in_notes = True
            continue
        if stripped.startswith("## ") and in_notes:
            break  # stop at the next section after Notes
        if stripped.startswith("## ") and not in_notes:
            break  # stop before any section header if no Notes yet
        if stripped.startswith("# "):
            continue  # skip the H1 title line
        if in_notes:
            if stripped.startswith("- "):
                notes_lines.append(stripped[2:].strip())
        else:
            prose_lines.append(line)

    prose = "\n".join(prose_lines).strip()
    notes = "\n".join(f"• {n}" for n in notes_lines if n)

    if prose and notes:
        return f"{prose}\n\n{notes}"
    return prose or notes


def _nullable_float(val: Any) -> float | None:
    if val is None:
        return None
    try:
        f = float(val)
        return f if f != 0.0 else None
    except (TypeError, ValueError):
        return None


def _extract_source_url(fm: dict) -> str | None:
    for key in SOURCE_KEYS:
        val = fm.get(key)
        if not val:
            continue
        if isinstance(val, list):
            return str(val[0]).strip() if val else None
        return str(val).strip()
    return None


_BOROUGH_CANON = {
    "manhattan": "Manhattan",
    "brooklyn": "Brooklyn",
    "queens": "Queens",
    "bronx": "Bronx",
    "the bronx": "Bronx",
    "staten island": "Staten Island",
}

def _canonical_borough(raw: Any) -> str | None:
    if not raw:
        return None
    return _BOROUGH_CANON.get(str(raw).strip().lower())


# ---------------------------------------------------------------------------
# API client helpers
# ---------------------------------------------------------------------------

def fetch_all_places(api_url: str) -> dict[str, dict]:
    """Return a {slug: place_dict} map of everything currently in the API."""
    existing: dict[str, dict] = {}
    page = 1
    while True:
        r = requests.get(
            f"{api_url}/api/places",
            params={"page": page, "per_page": 200},
            timeout=30,
        )
        r.raise_for_status()
        data = r.json()
        for place in data.get("data", []):
            existing[place["slug"]] = place
        if page >= data.get("total_pages", 1) or not data.get("data"):
            break
        page += 1
    return existing


def create_place(api_url: str, payload: dict) -> dict:
    r = requests.post(f"{api_url}/api/places", json=payload, timeout=30)
    r.raise_for_status()
    return r.json()


def update_place(api_url: str, place_id: str, payload: dict) -> dict:
    r = requests.put(f"{api_url}/api/places/{place_id}", json=payload, timeout=30)
    r.raise_for_status()
    return r.json()


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--wiki-root",
        default=str(Path.home() / "obsidian" / "LLM-Wiki" / "nyc"),
        help="Root of the NYC wiki (default: ~/obsidian/LLM-Wiki/nyc)",
    )
    parser.add_argument(
        "--api-url",
        default="http://localhost:8080",
        help="Base URL of the places API (default: http://localhost:8080)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Parse and print what would be synced without writing anything",
    )
    args = parser.parse_args()

    places_dir = Path(args.wiki_root) / PLACES_DIR_RELATIVE
    if not places_dir.is_dir():
        print(f"error: wiki places dir not found: {places_dir}", file=sys.stderr)
        return 1

    # Collect and parse wiki files.
    files = sorted(places_dir.glob("*.md"))
    parsed: list[tuple[Path, dict]] = []
    junk_skipped = 0
    parse_failures: list[ParseFailure] = []
    for f in files:
        if any(f.name.startswith(p) for p in JUNK_PREFIXES):
            junk_skipped += 1
            continue
        try:
            parsed.append((f, parse_wiki_file(f)))
        except ParseFailure as e:
            parse_failures.append(e)

    for e in parse_failures:
        print(f"parse-failed: {e.reason}", file=sys.stderr)
    print(
        f"Parsed {len(parsed)} places "
        f"({junk_skipped} junk-skipped, {len(parse_failures)} parse-failed) "
        f"from {places_dir}"
    )

    if args.dry_run:
        for _, p in parsed[:5]:
            print(f"  {p['slug']!r:40s} lat={p['lat']} tags={p['tags'][:3]}")
        if len(parsed) > 5:
            print(f"  … and {len(parsed) - 5} more")
        print("Dry run — nothing written.")
        return 1 if parse_failures else 0

    # Fetch existing places once.
    print(f"Fetching existing places from {args.api_url} …")
    try:
        existing = fetch_all_places(args.api_url)
    except requests.RequestException as e:
        print(f"error: could not reach API: {e}", file=sys.stderr)
        return 1
    print(f"Found {len(existing)} existing places in API.")

    created = updated = errors = 0

    for path, payload in parsed:
        slug = payload["slug"]
        try:
            if slug in existing:
                place_id = existing[slug]["id"]
                update_place(args.api_url, place_id, payload)
                updated += 1
            else:
                create_place(args.api_url, payload)
                created += 1
        except requests.HTTPError as e:
            body = e.response.text[:200] if e.response is not None else ""
            print(f"  error [{slug}]: {e} — {body}", file=sys.stderr)
            errors += 1
        except requests.RequestException as e:
            print(f"  error [{slug}]: {e}", file=sys.stderr)
            errors += 1

    # Orphan report — API records whose slug matches no parsed wiki
    # file. Report-only; never deletes or modifies anything.
    try:
        current = fetch_all_places(args.api_url)
    except requests.RequestException as e:
        print(f"error: could not refetch places for orphan report: {e}", file=sys.stderr)
        current = None

    orphans = 0
    if current is not None:
        wiki_slugs = {payload["slug"] for _, payload in parsed}
        for slug, place in sorted(current.items()):
            if slug not in wiki_slugs:
                orphans += 1
                name = place.get("name", "")
                place_id = place.get("id", "?")
                print(f"orphan: {slug} ({name}) [id={place_id}]", file=sys.stderr)

    print(
        f"\nDone: {created} created, {updated} updated, {errors} errors, "
        f"{orphans} orphans, {len(parse_failures)} parse-failed."
    )
    return 0 if errors == 0 and not parse_failures else 1


if __name__ == "__main__":
    sys.exit(main())
