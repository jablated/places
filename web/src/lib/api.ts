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
	tag?: string;
	city?: string;
	q?: string;
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
	if (params.tag) q.set('tag', params.tag);
	if (params.city) q.set('city', params.city);
	if (params.q) q.set('q', params.q);
	const s = q.toString();
	return s ? `?${s}` : '';
}

export const api = {
	listPlaces: (params: ListParams = {}, signal?: AbortSignal) =>
		request<ListResponse>(`/api/places${buildQuery(params)}`, { signal }),

	getPlace: (id: string) => request<Place>(`/api/places/${encodeURIComponent(id)}`),

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
		request<{ data: TagCount[]; total: number }>('/api/tags', { signal })
};
