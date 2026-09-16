<script lang="ts">
	import { onMount } from 'svelte';
	import { api, ApiError, type Place, type TagCount, type Trip, type TripDetail } from '$lib/api';
	import MapView from '$lib/components/MapView.svelte';
	import ListView from '$lib/components/ListView.svelte';
	import TripsView from '$lib/components/TripsView.svelte';
	import TripDetailView from '$lib/components/TripDetailView.svelte';

	type View = 'map' | 'list' | 'trips';

	// Map view needs every matching marker at once, so results are fetched a
	// page at a time and accumulated rather than paged through in the UI.
	const PER_PAGE = 100;
	const MAX_PAGES = 20; // 2000 places — a guard against an unbounded fetch loop
	const SEARCH_DEBOUNCE_MS = 250;
	const POPULAR_TAG_COUNT = 12;

	let view = $state<View>('map');
	let query = $state('');
	let activeTag = $state('');

	let places = $state<Place[]>([]);
	let tags = $state<TagCount[]>([]);
	let total = $state(0);
	let loading = $state(true);
	let error = $state('');
	let truncated = $state(false);

	// Trips are a separate, unfiltered resource, so they get their own state
	// rather than sharing the places request cycle. Loaded lazily the first time
	// the tab is opened — the Map and List views never need them.
	let trips = $state<Trip[]>([]);
	let tripsLoading = $state(false);
	let tripsError = $state('');
	let tripsLoaded = false;
	let openTrip = $state<TripDetail | null>(null);
	let openTripId = $state('');
	let tripDetailLoading = $state(false);
	let tripDetailError = $state('');

	// Only the newest in-flight request may write to state; an older one that
	// resolves late is aborted and its result discarded.
	let inflight: AbortController | null = null;
	let tripInflight: AbortController | null = null;
	let debounceTimer: ReturnType<typeof setTimeout> | null = null;

	async function load(q: string, tag: string) {
		inflight?.abort();
		const ctrl = new AbortController();
		inflight = ctrl;

		loading = true;
		error = '';

		try {
			const collected: Place[] = [];
			let page = 1;
			let totalPages = 1;
			let totalCount = 0;

			while (page <= totalPages && page <= MAX_PAGES) {
				const res = await api.listPlaces(
					{ page, per_page: PER_PAGE, q: q || undefined, tag: tag || undefined },
					ctrl.signal
				);
				collected.push(...res.data);
				totalPages = res.total_pages;
				totalCount = res.total;
				page += 1;
			}

			if (ctrl.signal.aborted) return;
			places = collected;
			total = totalCount;
			truncated = collected.length < totalCount;
		} catch (err) {
			if (ctrl.signal.aborted) return;
			error = err instanceof ApiError ? err.message : 'failed to load places';
			places = [];
			total = 0;
		} finally {
			if (inflight === ctrl) {
				inflight = null;
				loading = false;
			}
		}
	}

	async function loadTags() {
		try {
			const res = await api.listTags();
			tags = res.data;
		} catch {
			// The tag chips are a convenience; losing them shouldn't blank the page.
			tags = [];
		}
	}

	async function loadTrips() {
		tripsLoading = true;
		tripsError = '';
		try {
			const res = await api.listTrips();
			trips = res.data;
			tripsLoaded = true;
		} catch (err) {
			tripsError = err instanceof ApiError ? err.message : 'failed to load trips';
			trips = [];
		} finally {
			tripsLoading = false;
		}
	}

	async function openTripDetail(id: string) {
		tripInflight?.abort();
		const ctrl = new AbortController();
		tripInflight = ctrl;

		openTripId = id;
		openTrip = null;
		tripDetailLoading = true;
		tripDetailError = '';

		try {
			const detail = await api.getTrip(id, ctrl.signal);
			if (ctrl.signal.aborted) return;
			openTrip = detail;
		} catch (err) {
			if (ctrl.signal.aborted) return;
			tripDetailError = err instanceof ApiError ? err.message : 'failed to load the trip';
		} finally {
			if (tripInflight === ctrl) {
				tripInflight = null;
				tripDetailLoading = false;
			}
		}
	}

	function closeTripDetail() {
		tripInflight?.abort();
		tripInflight = null;
		tripDetailLoading = false;
		tripDetailError = '';
		openTripId = '';
		openTrip = null;
	}

	function showTrips() {
		view = 'trips';
		if (!tripsLoaded && !tripsLoading) loadTrips();
	}

	onMount(() => {
		loadTags();
		return () => {
			inflight?.abort();
			tripInflight?.abort();
			if (debounceTimer) clearTimeout(debounceTimer);
		};
	});

	// Refetch on filter change. The text input is debounced so typing doesn't
	// fire a request per keystroke; a tag click applies immediately.
	$effect(() => {
		const q = query;
		const tag = activeTag;

		if (debounceTimer) clearTimeout(debounceTimer);
		debounceTimer = setTimeout(() => load(q, tag), q ? SEARCH_DEBOUNCE_MS : 0);

		return () => {
			if (debounceTimer) clearTimeout(debounceTimer);
		};
	});

	function toggleTag(tag: string) {
		activeTag = activeTag === tag ? '' : tag;
	}

	function clearFilters() {
		query = '';
		activeTag = '';
	}

	const hasFilters = $derived(Boolean(query.trim() || activeTag));
	// The search box and tag chips filter places, so they are hidden on the trips
	// tab rather than sitting there doing nothing.
	const placesFilters = $derived(view !== 'trips');
	const popularTags = $derived(tags.slice(0, POPULAR_TAG_COUNT));
	// Only the map cares about this, but it's cheap and keeps the notice honest.
	const unmapped = $derived(places.filter((p) => p.lat == null || p.lng == null).length);
</script>

<svelte:head>
	<title>places</title>
	<meta name="description" content="A personal wiki of New York City places." />
</svelte:head>

<div class="app">
	<header class="bar">
		<div class="bar-row">
			<div class="toggle" role="group" aria-label="View">
				<button
					type="button"
					class="toggle-btn"
					class:active={view === 'map'}
					aria-pressed={view === 'map'}
					onclick={() => (view = 'map')}>Map</button
				>
				<button
					type="button"
					class="toggle-btn"
					class:active={view === 'list'}
					aria-pressed={view === 'list'}
					onclick={() => (view = 'list')}>List</button
				>
				<button
					type="button"
					class="toggle-btn"
					class:active={view === 'trips'}
					aria-pressed={view === 'trips'}
					onclick={showTrips}>Trips</button
				>
			</div>

			{#if placesFilters}
				<input
					class="search"
					type="search"
					placeholder="Search places…"
					aria-label="Search places"
					bind:value={query}
				/>
			{/if}

			<span class="count" aria-live="polite">
				{#if view === 'trips'}
					{#if tripsLoading}loading…{:else}{trips.length}
						{trips.length === 1 ? 'trip' : 'trips'}{/if}
				{:else if loading}loading…{:else}{total}
					{total === 1 ? 'place' : 'places'}{/if}
			</span>
		</div>

		{#if placesFilters && popularTags.length}
			<div class="chips">
				{#each popularTags as t (t.tag)}
					<button
						type="button"
						class="chip"
						class:active={activeTag === t.tag}
						aria-pressed={activeTag === t.tag}
						onclick={() => toggleTag(t.tag)}
					>
						{t.tag}<span class="chip-count">{t.count}</span>
					</button>
				{/each}
				{#if hasFilters}
					<button type="button" class="chip chip-clear" onclick={clearFilters}>clear ✕</button>
				{/if}
			</div>
		{/if}
	</header>

	<main class="content" class:content--map={view === 'map'}>
		{#if view === 'trips'}
			{#if openTripId}
				{#if tripDetailLoading}
					<div class="notice"><p>Loading trip…</p></div>
				{:else if tripDetailError}
					<div class="notice notice--error">
						<p>{tripDetailError}</p>
						<button type="button" onclick={() => openTripDetail(openTripId)}>Retry</button>
						<button type="button" onclick={closeTripDetail}>Back to trips</button>
					</div>
				{:else if openTrip}
					<TripDetailView trip={openTrip} onBack={closeTripDetail} />
				{/if}
			{:else if tripsLoading}
				<div class="notice"><p>Loading trips…</p></div>
			{:else if tripsError}
				<div class="notice notice--error">
					<p>{tripsError}</p>
					<button type="button" onclick={loadTrips}>Retry</button>
				</div>
			{:else if trips.length === 0}
				<div class="notice"><p>No trips yet.</p></div>
			{:else}
				<TripsView {trips} onSelect={(trip) => openTripDetail(trip.id)} />
			{/if}
		{:else if error}
			<div class="notice notice--error">
				<p>{error}</p>
				<button type="button" onclick={() => load(query, activeTag)}>Retry</button>
			</div>
		{:else if !loading && places.length === 0}
			<div class="notice">
				<p>
					{#if hasFilters}No places match that filter.{:else}No places yet.{/if}
				</p>
				{#if hasFilters}
					<button type="button" onclick={clearFilters}>Clear filters</button>
				{/if}
			</div>
		{:else if view === 'map'}
			<MapView {places} />
			{#if unmapped > 0}
				<p class="map-note">
					{unmapped}
					{unmapped === 1 ? 'place has' : 'places have'} no coordinates — see the list view.
				</p>
			{/if}
		{:else}
			<ListView {places} onTagClick={toggleTag} />
		{/if}

		{#if truncated && placesFilters}
			<p class="map-note map-note--warn">
				Showing the first {places.length} of {total} places.
			</p>
		{/if}
	</main>
</div>

<style>
	.app {
		display: flex;
		flex-direction: column;
		height: 100dvh;
		background: #fafafa;
	}

	.bar {
		flex-shrink: 0;
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		padding: 0.7rem 1rem;
		background: #fff;
		border-bottom: 1px solid #e4e4e7;
		z-index: 600; /* above the Leaflet canvas */
	}

	.bar-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.toggle {
		display: inline-flex;
		flex-shrink: 0;
		padding: 2px;
		border-radius: 8px;
		background: #f4f4f5;
	}

	.toggle-btn {
		padding: 0.35rem 0.85rem;
		border: 0;
		border-radius: 6px;
		background: transparent;
		color: #52525b;
		font: inherit;
		font-size: 0.85rem;
		cursor: pointer;
	}

	.toggle-btn.active {
		background: #fff;
		color: #18181b;
		font-weight: 600;
		box-shadow: 0 1px 2px rgb(0 0 0 / 0.08);
	}

	.search {
		flex: 1;
		min-width: 0;
		max-width: 420px;
		padding: 0.4rem 0.7rem;
		border: 1px solid #e4e4e7;
		border-radius: 8px;
		font: inherit;
		font-size: 0.88rem;
	}

	.search:focus {
		outline: 2px solid #a5b4fc;
		outline-offset: -1px;
	}

	.count {
		margin-left: auto;
		flex-shrink: 0;
		color: #a1a1aa;
		font-size: 0.78rem;
		font-variant-numeric: tabular-nums;
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
	}

	.chip {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0.2rem 0.6rem;
		border: 1px solid #e4e4e7;
		border-radius: 999px;
		background: #fff;
		color: #52525b;
		font: inherit;
		font-size: 0.75rem;
		cursor: pointer;
	}

	.chip:hover {
		border-color: #c7d2fe;
		background: #f5f7ff;
	}

	.chip.active {
		border-color: #4338ca;
		background: #4338ca;
		color: #fff;
	}

	.chip-count {
		color: #a1a1aa;
		font-variant-numeric: tabular-nums;
	}

	.chip.active .chip-count {
		color: #c7d2fe;
	}

	.chip-clear {
		color: #b3261e;
	}

	.content {
		position: relative;
		flex: 1;
		min-height: 0;
		overflow-y: auto;
	}

	/* The map fills its pane and scrolls internally, so the page must not. */
	.content--map {
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

	.notice button {
		padding: 0.35rem 0.9rem;
		border: 1px solid #e4e4e7;
		border-radius: 8px;
		background: #fff;
		font: inherit;
		font-size: 0.85rem;
		cursor: pointer;
	}

	.map-note {
		position: absolute;
		inset-block-end: 0.75rem;
		inset-inline-start: 0.75rem;
		z-index: 500;
		margin: 0;
		padding: 0.3rem 0.65rem;
		border-radius: 6px;
		background: rgb(255 255 255 / 0.92);
		color: #71717a;
		font-size: 0.72rem;
		box-shadow: 0 1px 3px rgb(0 0 0 / 0.12);
	}

	.map-note--warn {
		inset-block-end: 2.6rem;
		color: #92400e;
	}

	@media (max-width: 600px) {
		.bar-row {
			flex-wrap: wrap;
		}

		.search {
			max-width: none;
			order: 3;
			flex-basis: 100%;
		}
	}
</style>
