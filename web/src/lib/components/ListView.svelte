<script lang="ts">
	import type { Place } from '$lib/api';
	import { formatDate, hostOf, preview } from '$lib/format';
	import { haversineDistanceMi } from '$lib/geo';

	let {
		places = [],
		onTagClick,
		distanceFrom,
		onPlaceClick
	}: {
		places: Place[];
		onTagClick?: (tag: string) => void;
		/** When set, each card shows its distance from this point. */
		distanceFrom?: { lat: number; lng: number };
		/** When set, card titles become buttons that open the place. */
		onPlaceClick?: (id: string) => void;
	} = $props();
</script>

<div class="list">
	{#each places as place (place.id)}
		<article class="card">
			<header class="card-head">
				{#if onPlaceClick}
					<button type="button" class="card-title card-title-btn" onclick={() => onPlaceClick(place.id)}
						>{place.name}</button
					>
				{:else}
					<h2 class="card-title">{place.name}</h2>
				{/if}
				{#if place.visited}
					<span class="badge-visited" title="Visited">✓ visited</span>
				{/if}
			</header>

			<p class="card-meta">
				{#if place.neighborhood}<span>{place.neighborhood}</span>{/if}
				{#if place.neighborhood && place.city}<span class="dot">·</span>{/if}
				{#if place.city}<span>{place.city}</span>{/if}
				{#if place.created_at}
					<span class="dot">·</span><span class="date">{formatDate(place.created_at)}</span>
				{/if}
			</p>

			{#if distanceFrom && place.lat != null && place.lng != null}
				{@const dist = haversineDistanceMi(distanceFrom.lat, distanceFrom.lng, place.lat, place.lng)}
				<p class="card-dist">{dist < 0.1 ? `${Math.round(dist * 5280)} ft` : `${dist.toFixed(1)} mi`}</p>
			{/if}

			{#if place.tags.length}
				<p class="card-tags">
					{#each place.tags as tag (tag)}
						<button type="button" class="tag" onclick={() => onTagClick?.(tag)}>{tag}</button>
					{/each}
				</p>
			{/if}

			{#if place.description}
				<p class="card-desc">{preview(place.description)}</p>
			{/if}

			{#if place.source_url}
				<a class="card-link" href={place.source_url} target="_blank" rel="noopener noreferrer">
					{hostOf(place.source_url)} ↗
				</a>
			{/if}

			{#if place.lat != null && place.lng != null}
				<a
					class="card-nav"
					href="https://www.google.com/maps/dir/?api=1&destination={place.lat},{place.lng}"
					target="_blank"
					rel="noopener noreferrer">Navigate ↗</a
				>
			{/if}
		</article>
	{/each}
</div>

<style>
	.list {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
		gap: 1rem;
		padding: 1.25rem;
		max-width: 1400px;
		margin: 0 auto;
	}

	.card {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
		padding: 1rem 1.1rem;
		border: 1px solid #e4e4e7;
		border-radius: 12px;
		background: #fff;
		box-shadow: 0 1px 2px rgb(0 0 0 / 0.04);
	}

	.card-head {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.card-title {
		margin: 0;
		font-size: 1.05rem;
		font-weight: 600;
		line-height: 1.3;
	}

	.card-title-btn {
		border: 0;
		background: none;
		padding: 0;
		font: inherit;
		font-size: 1.05rem;
		font-weight: 600;
		line-height: 1.3;
		text-align: left;
		cursor: pointer;
		color: inherit;
	}

	.card-title-btn:hover {
		text-decoration: underline;
	}

	.badge-visited {
		flex-shrink: 0;
		color: #15803d;
		font-size: 0.72rem;
		font-weight: 500;
	}

	.card-meta {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin: 0;
		color: #71717a;
		font-size: 0.8rem;
	}

	.card-dist {
		margin: 0;
		color: #059669;
		font-size: 0.78rem;
		font-weight: 500;
	}

	.dot {
		color: #d4d4d8;
	}

	.date {
		color: #a1a1aa;
	}

	.card-tags {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin: 0.1rem 0 0;
	}

	.tag {
		padding: 0.15rem 0.5rem;
		border: 0;
		border-radius: 999px;
		background: #eef2ff;
		color: #4338ca;
		font-size: 0.72rem;
		font-family: inherit;
		cursor: pointer;
	}

	.tag:hover {
		background: #e0e7ff;
	}

	.card-desc {
		margin: 0;
		color: #3f3f46;
		font-size: 0.88rem;
		line-height: 1.5;
	}

	.card-link {
		margin-top: auto;
		padding-top: 0.2rem;
		color: #4338ca;
		font-size: 0.8rem;
		text-decoration: none;
		overflow-wrap: anywhere;
	}

	.card-link:hover {
		text-decoration: underline;
	}

	.card-nav {
		color: #059669;
		font-size: 0.8rem;
		text-decoration: none;
	}

	.card-nav:hover {
		text-decoration: underline;
	}
</style>
