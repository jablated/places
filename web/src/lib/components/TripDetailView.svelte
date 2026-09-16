<script lang="ts">
	import type { Place, TripDetail } from '$lib/api';
	import MapView, { type MapAnnotation } from '$lib/components/MapView.svelte';
	import { hostOf, plural, preview } from '$lib/format';
	import { routeColor } from '$lib/routeColors';

	let {
		trip,
		onBack
	}: { trip: TripDetail; onBack: () => void } = $props();

	// Segment colors are positional, so the swatch beside a route heading and the
	// pins for its stops are derived from the same index.
	const colorFor = $derived(
		new Map(trip.routes.map((route, i) => [route.label, routeColor(i)] as const))
	);

	// The map takes one flat list; this index gives the annotate callback back the
	// itinerary context that a bare Place has lost.
	const stops = $derived(trip.routes.flatMap((route) => route.stops));
	const stopById = $derived(new Map(stops.map((stop) => [stop.id, stop] as const)));

	function annotate(place: Place): MapAnnotation | undefined {
		const stop = stopById.get(place.id);
		if (!stop) return undefined;
		const label = stop.route || 'Stops';
		return { color: colorFor.get(label), label, note: stop.stop_notes || undefined };
	}

	const unmapped = $derived(stops.filter((s) => s.lat == null || s.lng == null).length);
	const durationLabel = $derived(
		trip.duration_days && trip.duration_days > 0 ? plural(trip.duration_days, 'day') : ''
	);
</script>

<div class="trip">
	<header class="head">
		<button type="button" class="back" onclick={onBack}>← All trips</button>

		<h1 class="title">{trip.name}</h1>

		<p class="meta">
			<span class="badge">{trip.trip_type}</span>
			<span>{plural(trip.place_count, 'stop')}</span>
			{#if durationLabel}
				<span class="dot">·</span><span>{durationLabel}</span>
			{/if}
			{#if trip.routes.length > 1}
				<span class="dot">·</span><span>{plural(trip.routes.length, 'route')}</span>
			{/if}
		</p>

		{#if trip.tags.length}
			<p class="tags">
				{#each trip.tags as tag (tag)}
					<span class="tag">{tag}</span>
				{/each}
			</p>
		{/if}

		{#if trip.description}
			<p class="desc">{trip.description}</p>
		{/if}

		{#if trip.source_url}
			<a class="source" href={trip.source_url} target="_blank" rel="noopener noreferrer">
				{hostOf(trip.source_url)} ↗
			</a>
		{/if}
	</header>

	<div class="map-pane">
		<!-- Keyed on the trip so switching itineraries rebuilds the map rather than
		     re-fitting a live one that the reader may have panned. -->
		{#key trip.id}
			<MapView places={stops} {annotate} fit />
		{/key}
		{#if unmapped > 0}
			<p class="map-note">
				{plural(unmapped, 'stop')} without coordinates — listed below.
			</p>
		{/if}
	</div>

	<div class="routes">
		{#each trip.routes as route (route.label)}
			<section class="route">
				<h2 class="route-head">
					<span class="swatch" style="background:{colorFor.get(route.label)}"></span>
					<span class="route-label">{route.label}</span>
					<span class="route-count">{plural(route.stops.length, 'stop')}</span>
				</h2>

				<ol class="stops">
					{#each route.stops as stop, i (stop.id)}
						<li class="stop">
							<span class="stop-num" style="background:{colorFor.get(route.label)}">{i + 1}</span>
							<div class="stop-body">
								<h3 class="stop-name">{stop.name}</h3>
								<p class="stop-meta">
									{#if stop.neighborhood}<span>{stop.neighborhood}</span>{/if}
									{#if stop.neighborhood && stop.city}<span class="dot">·</span>{/if}
									{#if stop.city}<span>{stop.city}</span>{/if}
									{#if stop.lat == null || stop.lng == null}
										<span class="dot">·</span><span class="unmapped">not on the map</span>
									{/if}
								</p>
								{#if stop.stop_notes}
									<p class="stop-notes">{stop.stop_notes}</p>
								{:else if stop.description}
									<p class="stop-notes stop-notes--fallback">{preview(stop.description, 160)}</p>
								{/if}
								{#if stop.source_url}
									<a
										class="stop-link"
										href={stop.source_url}
										target="_blank"
										rel="noopener noreferrer"
									>
										{hostOf(stop.source_url)} ↗
									</a>
								{/if}
							</div>
						</li>
					{/each}
				</ol>
			</section>
		{/each}

		{#if trip.routes.length === 0}
			<p class="empty">This trip has no stops yet.</p>
		{/if}
	</div>
</div>

<style>
	.trip {
		max-width: 1100px;
		margin: 0 auto;
		padding: 1.25rem 1.25rem 3rem;
	}

	.head {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.5rem;
	}

	.back {
		padding: 0.3rem 0.7rem 0.3rem 0.55rem;
		border: 1px solid #e4e4e7;
		border-radius: 8px;
		background: #fff;
		color: #52525b;
		font: inherit;
		font-size: 0.82rem;
		cursor: pointer;
	}

	.back:hover {
		border-color: #c7d2fe;
		color: #4338ca;
	}

	.title {
		margin: 0.2rem 0 0;
		font-size: 1.6rem;
		line-height: 1.2;
	}

	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem;
		margin: 0;
		color: #71717a;
		font-size: 0.85rem;
	}

	.badge {
		margin-right: 0.15rem;
		padding: 0.1rem 0.5rem;
		border-radius: 999px;
		background: #18181b;
		color: #fff;
		font-size: 0.68rem;
		font-weight: 500;
	}

	.dot {
		color: #d4d4d8;
	}

	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin: 0;
	}

	.tag {
		padding: 0.15rem 0.5rem;
		border-radius: 999px;
		background: #eef2ff;
		color: #4338ca;
		font-size: 0.72rem;
	}

	.desc {
		max-width: 62ch;
		margin: 0.2rem 0 0;
		color: #3f3f46;
		font-size: 0.92rem;
		line-height: 1.6;
		white-space: pre-line; /* the descriptions are markdown-ish; keep paragraphs */
	}

	.source {
		color: #4338ca;
		font-size: 0.82rem;
		text-decoration: none;
	}

	.source:hover {
		text-decoration: underline;
	}

	.map-pane {
		position: relative;
		height: min(46vh, 420px);
		margin: 1.1rem 0 1.5rem;
		border: 1px solid #e4e4e7;
		border-radius: 12px;
		overflow: hidden;
	}

	.map-note {
		position: absolute;
		inset-block-end: 0.7rem;
		inset-inline-start: 0.7rem;
		z-index: 500;
		margin: 0;
		padding: 0.3rem 0.65rem;
		border-radius: 6px;
		background: rgb(255 255 255 / 0.92);
		color: #71717a;
		font-size: 0.72rem;
		box-shadow: 0 1px 3px rgb(0 0 0 / 0.12);
	}

	.routes {
		display: flex;
		flex-direction: column;
		gap: 1.6rem;
	}

	.route-head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin: 0 0 0.7rem;
		font-size: 1rem;
		font-weight: 600;
	}

	.swatch {
		flex-shrink: 0;
		width: 12px;
		height: 12px;
		border-radius: 50%;
		box-shadow: 0 0 0 2px #fff;
	}

	.route-label {
		min-width: 0;
	}

	.route-count {
		color: #a1a1aa;
		font-size: 0.75rem;
		font-weight: 400;
	}

	.stops {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.stop {
		display: flex;
		gap: 0.7rem;
		padding: 0.8rem 1rem;
		border: 1px solid #e4e4e7;
		border-radius: 10px;
		background: #fff;
	}

	.stop-num {
		flex-shrink: 0;
		display: grid;
		place-items: center;
		width: 22px;
		height: 22px;
		border-radius: 50%;
		color: #fff;
		font-size: 0.72rem;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
	}

	.stop-body {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		min-width: 0;
	}

	.stop-name {
		margin: 0;
		font-size: 0.98rem;
		font-weight: 600;
		line-height: 1.3;
	}

	.stop-meta {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin: 0;
		color: #71717a;
		font-size: 0.78rem;
	}

	.unmapped {
		color: #92400e;
	}

	.stop-notes {
		margin: 0.15rem 0 0;
		color: #3f3f46;
		font-size: 0.86rem;
		line-height: 1.5;
	}

	/* No per-stop note written yet: fall back to the place's own description so
	   the row still says something, but mark it as borrowed. */
	.stop-notes--fallback {
		color: #71717a;
		font-style: italic;
	}

	.stop-link {
		margin-top: 0.15rem;
		color: #4338ca;
		font-size: 0.78rem;
		text-decoration: none;
		overflow-wrap: anywhere;
	}

	.stop-link:hover {
		text-decoration: underline;
	}

	.empty {
		color: #71717a;
	}

	@media (max-width: 600px) {
		.trip {
			padding-inline: 0.9rem;
		}

		.title {
			font-size: 1.35rem;
		}
	}
</style>
