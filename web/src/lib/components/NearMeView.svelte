<script lang="ts">
	import { onMount } from 'svelte';
	import { api, ApiError, type Place } from '$lib/api';
	import {
		bboxFromRadius,
		containsBBox,
		expandBBox,
		haversineDistanceMi,
		placeInBBox,
		type BBox
	} from '$lib/geo';
	import MapView from './MapView.svelte';
	import ListView from './ListView.svelte';

	type SubMode = 'map' | 'list';

	const DISPLAY_RADIUS_MI = 0.5;
	// The first fetch covers 4× the display radius (2 mi), so small pans and a
	// few zoom-outs are served from memory without another request.
	const CACHE_RADIUS_FACTOR = 4;
	const VIEWPORT_EXPAND_FACTOR = 2;
	// The API caps per_page at 100, so a bbox is fetched a page at a time.
	const PER_PAGE = 100;
	const MAX_PAGES = 10;

	let subMode = $state<SubMode>('map');
	let userLat = $state<number | null>(null);
	let userLng = $state<number | null>(null);
	let geoLoading = $state(true);
	let geoError = $state('');

	let cachedBBox = $state<BBox | null>(null);
	let cachedPlaces = $state<Place[]>([]);
	let fetchLoading = $state(false);
	let fetchError = $state('');
	let viewportBBox = $state<BBox | null>(null);
	let recenterTick = $state(0);

	// Only the newest fetch may write to the cache.
	let inflight: AbortController | null = null;

	const userLocation = $derived(
		userLat != null && userLng != null ? { lat: userLat, lng: userLng } : undefined
	);
	const homeBBox = $derived(
		userLocation
			? bboxFromRadius(userLocation.lat, userLocation.lng, DISPLAY_RADIUS_MI * CACHE_RADIUS_FACTOR)
			: null
	);

	async function fetchForBBox(bbox: BBox) {
		inflight?.abort();
		const ctrl = new AbortController();
		inflight = ctrl;
		fetchLoading = true;
		fetchError = '';

		try {
			const collected: Place[] = [];
			let page = 1;
			let totalPages = 1;
			while (page <= totalPages && page <= MAX_PAGES) {
				const res = await api.listPlaces(
					{
						page,
						per_page: PER_PAGE,
						sw_lat: bbox.swLat,
						sw_lng: bbox.swLng,
						ne_lat: bbox.neLat,
						ne_lng: bbox.neLng
					},
					ctrl.signal
				);
				collected.push(...res.data);
				totalPages = res.total_pages;
				page += 1;
			}
			if (ctrl.signal.aborted) return;
			cachedBBox = bbox;
			cachedPlaces = collected;
		} catch (err) {
			if (ctrl.signal.aborted) return;
			// Keep the previous cache on screen; a failed refresh shouldn't blank it.
			fetchError = err instanceof ApiError ? err.message : 'failed to load nearby places';
		} finally {
			if (inflight === ctrl) {
				inflight = null;
				fetchLoading = false;
			}
		}
	}

	function onViewportChange(bbox: BBox) {
		viewportBBox = bbox;
		if (cachedBBox && containsBBox(cachedBBox, bbox)) return;
		fetchForBBox(expandBBox(bbox, VIEWPORT_EXPAND_FACTOR));
	}

	function setSubMode(mode: SubMode) {
		subMode = mode;
		// Panning the map far away replaces the cache with that area, so the list
		// (always centered on the user) needs the home area back.
		if (mode === 'list' && homeBBox && !(cachedBBox && containsBBox(cachedBBox, homeBBox))) {
			fetchForBBox(homeBBox);
		}
	}

	function recenter() {
		recenterTick += 1;
	}

	const visiblePlaces = $derived.by(() => {
		if (!userLocation) return [];
		const withCoords = cachedPlaces.filter(
			(p): p is Place & { lat: number; lng: number } => p.lat != null && p.lng != null
		);

		if (subMode === 'list') {
			return withCoords
				.map((p) => ({
					p,
					d: haversineDistanceMi(userLocation.lat, userLocation.lng, p.lat, p.lng)
				}))
				.filter((x) => x.d <= DISPLAY_RADIUS_MI)
				.sort((a, b) => a.d - b.d)
				.map((x) => x.p);
		}

		const box =
			viewportBBox ?? bboxFromRadius(userLocation.lat, userLocation.lng, DISPLAY_RADIUS_MI);
		return withCoords.filter((p) => placeInBBox(p.lat, p.lng, box));
	});

	onMount(() => {
		if (!('geolocation' in navigator)) {
			geoError = 'Location is not supported by this browser.';
			geoLoading = false;
			return;
		}

		navigator.geolocation.getCurrentPosition(
			(pos) => {
				userLat = pos.coords.latitude;
				userLng = pos.coords.longitude;
				geoLoading = false;
				fetchForBBox(
					bboxFromRadius(userLat, userLng, DISPLAY_RADIUS_MI * CACHE_RADIUS_FACTOR)
				);
			},
			(err) => {
				geoError =
					err.code === 1
						? 'Location access denied. Allow location for this site to see nearby places.'
						: 'Could not get location';
				geoLoading = false;
			},
			{ timeout: 10000, maximumAge: 60000 }
		);

		return () => inflight?.abort();
	});
</script>

{#if geoLoading}
	<div class="notice">
		<span class="spinner" aria-hidden="true"></span>
		<p>Finding your location…</p>
	</div>
{:else if geoError}
	<div class="notice notice--error"><p>{geoError}</p></div>
{:else if userLocation}
	<div class="nearme">
		<div class="sub-bar">
			<div class="toggle" role="group" aria-label="Near me view">
				<button
					type="button"
					class="toggle-btn"
					class:active={subMode === 'map'}
					aria-pressed={subMode === 'map'}
					onclick={() => setSubMode('map')}>Map</button
				>
				<button
					type="button"
					class="toggle-btn"
					class:active={subMode === 'list'}
					aria-pressed={subMode === 'list'}
					onclick={() => setSubMode('list')}>List</button
				>
			</div>

			{#if subMode === 'map'}
				<button type="button" class="recenter" onclick={recenter}>◎ Recenter</button>
			{/if}

			<span class="count" aria-live="polite">
				{#if fetchLoading}loading…{:else}{visiblePlaces.length}
					{visiblePlaces.length === 1 ? 'place' : 'places'}
					{subMode === 'list' ? `within ${DISPLAY_RADIUS_MI} mi` : 'in view'}{/if}
			</span>
		</div>

		{#if fetchError}
			<p class="fetch-error">{fetchError}</p>
		{/if}

		<div class="nearme-body" class:nearme-map={subMode === 'map'}>
			{#if subMode === 'map'}
				<MapView places={visiblePlaces} {userLocation} {onViewportChange} {recenterTick} />
			{:else if !fetchLoading && visiblePlaces.length === 0}
				<div class="notice"><p>No places within {DISPLAY_RADIUS_MI} mi.</p></div>
			{:else}
				<ListView places={visiblePlaces} distanceFrom={userLocation} />
			{/if}
		</div>
	</div>
{/if}

<style>
	.nearme {
		display: flex;
		flex-direction: column;
		height: 100%;
	}

	.sub-bar {
		flex-shrink: 0;
		display: flex;
		align-items: center;
		gap: 0.6rem;
		padding: 0.45rem 1rem;
		background: #fff;
		border-bottom: 1px solid #e4e4e7;
	}

	.toggle {
		display: inline-flex;
		padding: 2px;
		border-radius: 8px;
		background: #f4f4f5;
	}

	.toggle-btn {
		padding: 0.25rem 0.7rem;
		border: 0;
		border-radius: 6px;
		background: transparent;
		color: #52525b;
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
	}

	.toggle-btn.active {
		background: #fff;
		color: #18181b;
		font-weight: 600;
		box-shadow: 0 1px 2px rgb(0 0 0 / 0.08);
	}

	.recenter {
		padding: 0.25rem 0.7rem;
		border: 1px solid #e4e4e7;
		border-radius: 8px;
		background: #fff;
		color: #4338ca;
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
	}

	.recenter:hover {
		border-color: #c7d2fe;
		background: #f5f7ff;
	}

	.count {
		margin-left: auto;
		color: #a1a1aa;
		font-size: 0.78rem;
		font-variant-numeric: tabular-nums;
	}

	.fetch-error {
		flex-shrink: 0;
		margin: 0;
		padding: 0.35rem 1rem;
		background: #fef2f2;
		color: #b3261e;
		font-size: 0.8rem;
	}

	.nearme-body {
		flex: 1;
		min-height: 0;
		overflow-y: auto;
	}

	/* The map scrolls internally, so its pane must not. */
	.nearme-map {
		overflow: hidden;
	}

	.notice {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.6rem;
		padding: 3rem 1rem;
		color: #71717a;
		text-align: center;
	}

	.notice p {
		margin: 0;
	}

	.notice--error {
		color: #b3261e;
	}

	.spinner {
		width: 22px;
		height: 22px;
		border: 3px solid #e0e7ff;
		border-top-color: #4338ca;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
