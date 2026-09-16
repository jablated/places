<script module lang="ts">
	/**
	 * Optional per-marker decoration. `annotate` is how the trip view tints pins
	 * by route segment and adds the route label and stop notes to the popup,
	 * without this component needing to know what a trip is. Both extra props
	 * default to off, so the plain Map view renders exactly as before.
	 */
	export interface MapAnnotation {
		/** Pin fill, as #rgb or #rrggbb. Anything else is ignored. */
		color?: string;
		/** A short line above the locality — the route segment, in practice. */
		label?: string;
		/** Free text appended to the popup. Escaped here, so pass it raw. */
		note?: string;
	}
</script>

<script lang="ts">
	import { onMount } from 'svelte';
	import type { Place } from '$lib/api';
	import type { BBox } from '$lib/geo';

	let {
		places = [],
		annotate,
		fit = false,
		userLocation,
		onViewportChange,
		recenterTick,
		onPlaceClick
	}: {
		places: Place[];
		annotate?: (place: Place) => MapAnnotation | undefined;
		/** Frame the markers on load instead of using the default NYC viewport. */
		fit?: boolean;
		/** Draws a "you are here" pin with a half-mile ring. */
		userLocation?: { lat: number; lng: number };
		/** Called with the visible bounds once panning/zooming settles. */
		onViewportChange?: (bbox: BBox) => void;
		/** Bump to fly back to userLocation. */
		recenterTick?: number;
		/** Called with a place id when its popup title is clicked. */
		onPlaceClick?: (id: string) => void;
	} = $props();

	// NYC default viewport, per spec.
	const DEFAULT_CENTER: [number, number] = [40.7128, -74.006];
	const DEFAULT_ZOOM = 12;
	const FIT_PADDING: [number, number] = [40, 40];
	const FIT_MAX_ZOOM = 15;
	const USER_RADIUS_M = 804; // half a mile
	const USER_ZOOM = 15;
	const VIEWPORT_DEBOUNCE_MS = 2000;

	// OpenStreetMap raster tiles — free, no API key, no WebGL required.
	const TILE_URL = 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png';
	const TILE_ATTRIBUTION =
		'© <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a> contributors';

	let container: HTMLDivElement;
	// Leaflet and MapLibre both touch `window` at import time, so they are
	// imported dynamically inside onMount and typed as any here.
	let L: any = null;
	let map: any = null;
	let markerLayer: any = null;
	let userCircle: any = null;
	let userMarker: any = null;
	let error = $state('');

	function escapeHtml(s: string): string {
		return s.replace(
			/[&<>"']/g,
			(c) =>
				({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string
		);
	}

	/**
	 * Colors are written into a style attribute, so only a literal hex value is
	 * allowed through — callers supply them from their own palette, but this
	 * component shouldn't be the one place a future caller can inject CSS.
	 */
	const HEX_COLOR = /^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i;

	function popupHtml(p: Place, ann?: MapAnnotation): string {
		// No href: the click is caught by the delegated listener in onMount so
		// Svelte handles it. p.id is a UUID, so it is safe to inline unescaped.
		const parts = [
			`<h3 class="popup-title"><a class="popup-title-link" data-place-id="${p.id}">${escapeHtml(p.name)}</a></h3>`
		];

		if (ann?.label) parts.push(`<p class="popup-route">${escapeHtml(ann.label)}</p>`);

		const locality = [p.neighborhood, p.city].filter(Boolean).join(', ');
		if (locality) parts.push(`<p class="popup-meta">${escapeHtml(locality)}</p>`);

		if (ann?.note) parts.push(`<p class="popup-note">${escapeHtml(ann.note)}</p>`);

		if (p.tags.length) {
			const chips = p.tags.map((t) => `<span class="popup-tag">${escapeHtml(t)}</span>`).join('');
			parts.push(`<p class="popup-tags">${chips}</p>`);
		}

		if (p.visited) parts.push('<p class="popup-visited">✓ visited</p>');

		if (p.source_url) {
			// rel=noopener because the popup opens a third-party link in a new tab.
			parts.push(
				`<p><a class="popup-link" href="${escapeHtml(p.source_url)}" target="_blank" rel="noopener noreferrer">source ↗</a></p>`
			);
		}

		if (p.lat != null && p.lng != null) {
			parts.push(
				`<p><a class="popup-nav" href="https://www.google.com/maps/dir/?api=1&destination=${p.lat},${p.lng}" target="_blank" rel="noopener noreferrer">Navigate ↗</a></p>`
			);
		}

		return `<div class="popup">${parts.join('')}</div>`;
	}

	/** A small CSS-only pin, so no marker image assets need bundling. */
	function pinIcon(visited: boolean, color?: string) {
		const style = color && HEX_COLOR.test(color) ? ` style="background:${color}"` : '';
		return L.divIcon({
			className: 'place-pin-wrap',
			html: `<span class="place-pin${visited ? ' place-pin--visited' : ''}"${style}></span>`,
			iconSize: [18, 18],
			iconAnchor: [9, 9],
			popupAnchor: [0, -10]
		});
	}

	/** Rebuilds the marker layer. Cheap at this scale and avoids diffing state. */
	function renderMarkers(list: Place[]) {
		if (!map || !markerLayer) return;
		markerLayer.clearLayers();
		const points: [number, number][] = [];
		for (const p of list) {
			// Places without coordinates are wiki entries nobody has pinned yet;
			// they show up in the list view but have nowhere to go on the map.
			if (p.lat == null || p.lng == null) continue;
			const ann = annotate?.(p);
			L.marker([p.lat, p.lng], { icon: pinIcon(p.visited, ann?.color), title: p.name })
				.bindPopup(popupHtml(p, ann))
				.addTo(markerLayer);
			points.push([p.lat, p.lng]);
		}
		// maxZoom keeps a single-marker (or tightly clustered) trip from zooming
		// to street level, where there is no context left to read.
		if (fit && points.length) {
			map.fitBounds(L.latLngBounds(points), { padding: FIT_PADDING, maxZoom: FIT_MAX_ZOOM });
		}
	}

	onMount(() => {
		let disposed = false;
		let viewportTimer: ReturnType<typeof setTimeout> | null = null;
		let onMoveEnd: (() => void) | null = null;

		// Popups are raw HTML outside Svelte, so title clicks are delegated from
		// the map container back to the onPlaceClick prop.
		const handlePopupClick = (e: MouseEvent) => {
			const a = (e.target as HTMLElement).closest('[data-place-id]');
			if (!a) return;
			e.preventDefault();
			const id = a.getAttribute('data-place-id');
			if (id) onPlaceClick?.(id);
		};

		(async () => {
			try {
				const leaflet = await import('leaflet');
				await import('leaflet/dist/leaflet.css');

				if (disposed) return;
				L = leaflet.default ?? leaflet;

				map = L.map(container, {
					center: DEFAULT_CENTER,
					zoom: DEFAULT_ZOOM,
					zoomControl: true,
					attributionControl: true
				});

				L.tileLayer(TILE_URL, { attribution: TILE_ATTRIBUTION, maxZoom: 19 }).addTo(map);

				if (userLocation) {
					const at: [number, number] = [userLocation.lat, userLocation.lng];
					map.setView(at, USER_ZOOM);
					userCircle = L.circle(at, {
						radius: USER_RADIUS_M,
						color: '#4338ca',
						fillColor: '#4338ca',
						fillOpacity: 0.08,
						weight: 2
					}).addTo(map);
					userMarker = L.marker(at, {
						icon: L.divIcon({ className: 'user-location-pin', iconSize: [16, 16], iconAnchor: [8, 8] }),
						title: 'You are here',
						keyboard: false,
						zIndexOffset: 1000
					}).addTo(map);
				}

				if (onViewportChange) {
					// Debounced so a drag-and-zoom gesture produces one fetch, not a burst.
					onMoveEnd = () => {
						if (viewportTimer) clearTimeout(viewportTimer);
						viewportTimer = setTimeout(() => {
							if (!map) return;
							const b = map.getBounds();
							onViewportChange({
								swLat: b.getSouth(),
								swLng: b.getWest(),
								neLat: b.getNorth(),
								neLng: b.getEast()
							});
						}, VIEWPORT_DEBOUNCE_MS);
					};
					map.on('moveend zoomend', onMoveEnd);
				}

				markerLayer = L.layerGroup().addTo(map);
				renderMarkers(places);
				container.addEventListener('click', handlePopupClick);
			} catch (err) {
				error = err instanceof Error ? err.message : 'failed to load the map';
			}
		})();

		return () => {
			disposed = true;
			container.removeEventListener('click', handlePopupClick);
			if (viewportTimer) clearTimeout(viewportTimer);
			if (onMoveEnd) map?.off('moveend zoomend', onMoveEnd);
			map?.remove();
			map = null;
			markerLayer = null;
			userCircle = null;
			userMarker = null;
		};
	});

	// Re-render pins whenever the filtered result set changes. Guarded on `map`
	// because this fires before onMount's async import resolves.
	$effect(() => {
		const list = places;
		// Read annotate as well, so switching trips re-tints the pins even when
		// the stop list happens to be reference-equal.
		void annotate;
		if (map) renderMarkers(list);
	});

	$effect(() => {
		if (recenterTick && recenterTick > 0 && map && userLocation) {
			map.setView([userLocation.lat, userLocation.lng], USER_ZOOM);
		}
	});
</script>

<div class="map-wrap">
	<div class="map" bind:this={container}></div>

	{#if error}
		<p class="map-error">Map failed to load: {error}</p>
	{/if}
</div>

<style>
	.map-wrap {
		position: relative;
		height: 100%;
		width: 100%;
	}

	.map {
		height: 100%;
		width: 100%;
		background: #e8e6e1;
	}

	.map-error {
		position: absolute;
		inset-block-start: 1rem;
		inset-inline: 1rem;
		z-index: 500;
		margin: 0;
		padding: 0.75rem 1rem;
		border-radius: 8px;
		background: #b3261e;
		color: #fff;
		font-size: 0.85rem;
	}

	/* Marker and popup internals are injected as raw HTML by Leaflet, so they
	   sit outside Svelte's scoped-style rewriting — hence :global. */
	:global(.place-pin) {
		display: block;
		width: 18px;
		height: 18px;
		border-radius: 50%;
		background: #e5484d;
		border: 3px solid #fff;
		box-shadow: 0 1px 4px rgb(0 0 0 / 0.4);
		cursor: pointer;
	}

	:global(.user-location-pin) {
		display: block;
		width: 16px;
		height: 16px;
		border-radius: 50%;
		background: #4338ca;
		border: 3px solid #fff;
		box-shadow: 0 0 0 3px rgba(67, 56, 202, 0.3);
	}

	:global(.place-pin--visited) {
		background: #30a46c;
	}

	:global(.popup) {
		min-width: 170px;
		font:
			14px/1.45 system-ui,
			sans-serif;
	}

	:global(.popup-title) {
		margin: 0 0 0.2rem;
		font-size: 1rem;
	}

	:global(.popup-title-link) {
		color: inherit;
		text-decoration: none;
		cursor: pointer;
	}

	:global(.popup-title-link:hover) {
		text-decoration: underline;
	}

	:global(.popup-route) {
		margin: 0 0 0.25rem;
		color: #4338ca;
		font-size: 0.76rem;
		font-weight: 600;
	}

	:global(.popup-meta) {
		margin: 0 0 0.4rem;
		color: #666;
		font-size: 0.8rem;
	}

	:global(.popup-note) {
		margin: 0 0 0.45rem;
		color: #3f3f46;
		font-size: 0.8rem;
		line-height: 1.45;
	}

	:global(.popup-tags) {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
		margin: 0 0 0.4rem;
	}

	:global(.popup-tag) {
		padding: 0.1rem 0.4rem;
		border-radius: 999px;
		background: #eceaff;
		color: #4338ca;
		font-size: 0.72rem;
	}

	:global(.popup-visited) {
		margin: 0 0 0.4rem;
		color: #30a46c;
		font-size: 0.78rem;
	}

	:global(.popup-link) {
		color: #4338ca;
		font-size: 0.82rem;
	}

	:global(.popup-nav) {
		color: #059669;
		font-size: 0.82rem;
	}
</style>
