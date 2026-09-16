<script lang="ts">
	import { onMount } from 'svelte';
	import type { Place } from '$lib/api';

	let { places = [] }: { places: Place[] } = $props();

	// NYC default viewport, per spec.
	const DEFAULT_CENTER: [number, number] = [40.7128, -74.006];
	const DEFAULT_ZOOM = 12;

	// OpenFreeMap's Liberty style — free, no API key. Note this is a MapLibre
	// *vector style* document, not an {x}/{y}/{z} raster template, so it is
	// rendered through the maplibre-gl-leaflet bridge below rather than a plain
	// L.tileLayer. Everything else (markers, popups, panning) stays vanilla
	// Leaflet.
	const STYLE_URL = 'https://tiles.openfreemap.org/styles/liberty';

	let container: HTMLDivElement;
	// Leaflet and MapLibre both touch `window` at import time, so they are
	// imported dynamically inside onMount and typed as any here.
	let L: any = null;
	let map: any = null;
	let markerLayer: any = null;
	let error = $state('');

	function escapeHtml(s: string): string {
		return s.replace(
			/[&<>"']/g,
			(c) =>
				({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string
		);
	}

	function popupHtml(p: Place): string {
		const parts = [`<h3 class="popup-title">${escapeHtml(p.name)}</h3>`];

		const locality = [p.neighborhood, p.city].filter(Boolean).join(', ');
		if (locality) parts.push(`<p class="popup-meta">${escapeHtml(locality)}</p>`);

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

		return `<div class="popup">${parts.join('')}</div>`;
	}

	/** A small CSS-only pin, so no marker image assets need bundling. */
	function pinIcon(visited: boolean) {
		return L.divIcon({
			className: 'place-pin-wrap',
			html: `<span class="place-pin${visited ? ' place-pin--visited' : ''}"></span>`,
			iconSize: [18, 18],
			iconAnchor: [9, 9],
			popupAnchor: [0, -10]
		});
	}

	/** Rebuilds the marker layer. Cheap at this scale and avoids diffing state. */
	function renderMarkers(list: Place[]) {
		if (!map || !markerLayer) return;
		markerLayer.clearLayers();
		for (const p of list) {
			// Places without coordinates are wiki entries nobody has pinned yet;
			// they show up in the list view but have nowhere to go on the map.
			if (p.lat == null || p.lng == null) continue;
			L.marker([p.lat, p.lng], { icon: pinIcon(p.visited), title: p.name })
				.bindPopup(popupHtml(p))
				.addTo(markerLayer);
		}
	}

	onMount(() => {
		let disposed = false;

		(async () => {
			try {
				const leaflet = await import('leaflet');
				await import('leaflet/dist/leaflet.css');
				await import('maplibre-gl/dist/maplibre-gl.css');
				// Registers L.maplibreGL as a side effect; must come after Leaflet.
				await import('@maplibre/maplibre-gl-leaflet');

				if (disposed) return;
				L = leaflet.default ?? leaflet;

				map = L.map(container, {
					center: DEFAULT_CENTER,
					zoom: DEFAULT_ZOOM,
					zoomControl: true,
					attributionControl: true
				});

				L.maplibreGL({ style: STYLE_URL }).addTo(map);
				map.attributionControl.addAttribution(
					'<a href="https://openfreemap.org" target="_blank" rel="noopener">OpenFreeMap</a> · ' +
						'<a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OSM</a>'
				);

				markerLayer = L.layerGroup().addTo(map);
				renderMarkers(places);
			} catch (err) {
				error = err instanceof Error ? err.message : 'failed to load the map';
			}
		})();

		return () => {
			disposed = true;
			map?.remove();
			map = null;
			markerLayer = null;
		};
	});

	// Re-render pins whenever the filtered result set changes. Guarded on `map`
	// because this fires before onMount's async import resolves.
	$effect(() => {
		const list = places;
		if (map) renderMarkers(list);
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

	:global(.popup-meta) {
		margin: 0 0 0.4rem;
		color: #666;
		font-size: 0.8rem;
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
</style>
