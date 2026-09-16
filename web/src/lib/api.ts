// Typed client for the Go API. In production the SvelteKit bundle is embedded
// in the same binary, so relative /api URLs are same-origin. In dev the Vite
// server runs on another port, hence PUBLIC_API_BASE / VITE_API_BASE.

export interface Place {
	id: string;
	name: string;
	slug: string;
	description: string;
	source_url: string;
	lat: number | null;
	lng: number | null;
	city: string;
	neighborhood: string;
	tags: string[];
	visited: boolean;
	created_at: string;
	updated_at: string;
}

export interface TagCount {
	tag: string;
	count: number;
}

export interface ListResponse {
	data: Place[];
	total: number;
	page: number;
	per_page: number;
	total_pages: number;
}

export interface ListParams {
	page?: number;
	per_page?: number;
	tags?: string[];
	city?: string;
	q?: string;
	/** Bounding box; the API applies it only when all four are present. */
	sw_lat?: number;
	sw_lng?: number;
	ne_lat?: number;
	ne_lng?: number;
}

/** A named collection of places, optionally grouped into route segments. */
export interface Trip {
	id: string;
	name: string;
	slug: string;
	description: string;
	trip_type: string;
	tags: string[];
	duration_days: number | null;
	/** Live count of stops on the trip, computed server-side per query. */
	place_count: number;
	source_url: string;
	created_at: string;
	updated_at: string;
}

/** A place as it appears on a trip: the whole place, plus its itinerary context. */
export interface TripStop extends Place {
	route: string;
	route_order: number;
	stop_notes: string;
}

/** One segment of a trip. Stops with no route label arrive under label "Stops". */
export interface TripRoute {
	label: string;
	stops: TripStop[];
}

/** GET /api/trips/{id} — a trip with its stops already grouped by segment. */
export interface TripDetail extends Trip {
	routes: TripRoute[];
}

/**
 * The writable subset of a trip. Separate from `Partial<Trip>` because the API
 * rejects unknown fields, so posting a whole Trip back (id, place_count, …)
 * would 400.
 *
 * `duration_days` is omit-to-keep; send 0 to clear it.
 */
export interface TripInput {
	name?: string;
	slug?: string;
	description?: string;
	trip_type?: string;
	tags?: string[];
	duration_days?: number;
	source_url?: string;
}

/** Body of POST /api/trips/{id}/places. Re-posting an existing stop updates it. */
export interface TripStopInput {
	place_id: string;
	route?: string;
	route_order?: number;
	stop_notes?: string;
}

// Vite inlines import.meta.env at build time; empty means same-origin.
const API_BASE = (import.meta.env.VITE_API_BASE ?? '').replace(/\/$/, '');

/** Thrown for any non-2xx response, carrying the status and the API's message. */
export class ApiError extends Error {
	constructor(
		readonly status: number,
		message: string
	) {
		super(message);
		this.name = 'ApiError';
	}
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	let res: Response;
	try {
		res = await fetch(`${API_BASE}${path}`, {
			...init,
			headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) }
		});
	} catch (err) {
		// A network-level failure has no status; surface it as 0 so callers can
		// tell "server unreachable" from "server said no".
		throw new ApiError(0, err instanceof Error ? err.message : 'network error');
	}

	if (res.status === 204) return undefined as T;

	if (!res.ok) {
		// Error bodies are JSON {error: "..."} but a proxy or a crash can return
		// HTML, so fall back to the status text rather than throwing on parse.
		let message = res.statusText || `request failed (${res.status})`;
		try {
			const body = await res.json();
			if (body && typeof body.error === 'string') message = body.error;
		} catch {
			/* keep the status-text fallback */
		}
		throw new ApiError(res.status, message);
	}

	return (await res.json()) as T;
}

function buildQuery(params: ListParams): string {
	const q = new URLSearchParams();
	if (params.page && params.page > 1) q.set('page', String(params.page));
	if (params.per_page) q.set('per_page', String(params.per_page));
	for (const tag of params.tags ?? []) q.append('tag', tag);
	if (params.city) q.set('city', params.city);
	if (params.q) q.set('q', params.q);
	if (params.sw_lat != null) q.set('sw_lat', String(params.sw_lat));
	if (params.sw_lng != null) q.set('sw_lng', String(params.sw_lng));
	if (params.ne_lat != null) q.set('ne_lat', String(params.ne_lat));
	if (params.ne_lng != null) q.set('ne_lng', String(params.ne_lng));
	const s = q.toString();
	return s ? `?${s}` : '';
}

export const api = {
	listPlaces: (params: ListParams = {}, signal?: AbortSignal) =>
		request<ListResponse>(`/api/places${buildQuery(params)}`, { signal }),

	getPlace: (id: string, signal?: AbortSignal) =>
		request<Place>(`/api/places/${encodeURIComponent(id)}`, { signal }),

	createPlace: (place: Partial<Place>) =>
		request<Place>('/api/places', { method: 'POST', body: JSON.stringify(place) }),

	updatePlace: (id: string, patch: Partial<Place>) =>
		request<Place>(`/api/places/${encodeURIComponent(id)}`, {
			method: 'PUT',
			body: JSON.stringify(patch)
		}),

	deletePlace: (id: string) =>
		request<void>(`/api/places/${encodeURIComponent(id)}`, { method: 'DELETE' }),

	listTags: (signal?: AbortSignal) =>
		request<{ data: TagCount[]; total: number }>('/api/tags', { signal }),

	listTrips: (signal?: AbortSignal) =>
		request<{ data: Trip[]; total: number }>('/api/trips', { signal }),

	getTrip: (id: string, signal?: AbortSignal) =>
		request<TripDetail>(`/api/trips/${encodeURIComponent(id)}`, { signal }),

	getTripBySlug: (slug: string, signal?: AbortSignal) =>
		request<TripDetail>(`/api/trips/slug/${encodeURIComponent(slug)}`, { signal }),

	createTrip: (trip: TripInput) =>
		request<Trip>('/api/trips', { method: 'POST', body: JSON.stringify(trip) }),

	updateTrip: (id: string, patch: TripInput) =>
		request<Trip>(`/api/trips/${encodeURIComponent(id)}`, {
			method: 'PUT',
			body: JSON.stringify(patch)
		}),

	deleteTrip: (id: string) =>
		request<void>(`/api/trips/${encodeURIComponent(id)}`, { method: 'DELETE' }),

	addTripPlace: (tripId: string, stop: TripStopInput) =>
		request<TripStop>(`/api/trips/${encodeURIComponent(tripId)}/places`, {
			method: 'POST',
			body: JSON.stringify(stop)
		}),

	removeTripPlace: (tripId: string, placeId: string) =>
		request<void>(
			`/api/trips/${encodeURIComponent(tripId)}/places/${encodeURIComponent(placeId)}`,
			{ method: 'DELETE' }
		)
};
