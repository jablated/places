<script lang="ts">
	import type { Trip } from '$lib/api';
	import { plural, preview } from '$lib/format';

	let {
		trips = [],
		onSelect
	}: { trips: Trip[]; onSelect: (trip: Trip) => void } = $props();

	function durationLabel(days: number | null): string {
		return days && days > 0 ? plural(days, 'day') : '';
	}
</script>

<div class="list">
	{#each trips as trip (trip.id)}
		<!-- The whole card is the control: a trip has no secondary action, so a
		     separate "open" link would just be a second way to do one thing. -->
		<button type="button" class="card" onclick={() => onSelect(trip)}>
			<header class="card-head">
				<h2 class="card-title">{trip.name}</h2>
				<span class="badge">{trip.trip_type}</span>
			</header>

			<p class="card-meta">
				<span>{plural(trip.place_count, 'stop')}</span>
				{#if durationLabel(trip.duration_days)}
					<span class="dot">·</span><span>{durationLabel(trip.duration_days)}</span>
				{/if}
			</p>

			{#if trip.tags.length}
				<p class="card-tags">
					{#each trip.tags as tag (tag)}
						<span class="tag">{tag}</span>
					{/each}
				</p>
			{/if}

			{#if trip.description}
				<p class="card-desc">{preview(trip.description)}</p>
			{/if}

			<span class="card-open">View itinerary →</span>
		</button>
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
		align-items: stretch;
		gap: 0.45rem;
		padding: 1rem 1.1rem;
		border: 1px solid #e4e4e7;
		border-radius: 12px;
		background: #fff;
		box-shadow: 0 1px 2px rgb(0 0 0 / 0.04);
		font: inherit;
		text-align: start;
		cursor: pointer;
	}

	.card:hover {
		border-color: #c7d2fe;
		box-shadow: 0 2px 8px rgb(67 56 202 / 0.1);
	}

	.card:focus-visible {
		outline: 2px solid #4338ca;
		outline-offset: 2px;
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

	.badge {
		flex-shrink: 0;
		padding: 0.1rem 0.5rem;
		border-radius: 999px;
		background: #18181b;
		color: #fff;
		font-size: 0.68rem;
		font-weight: 500;
		letter-spacing: 0.02em;
	}

	.card-meta {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin: 0;
		color: #71717a;
		font-size: 0.8rem;
	}

	.dot {
		color: #d4d4d8;
	}

	.card-tags {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin: 0.1rem 0 0;
	}

	.tag {
		padding: 0.15rem 0.5rem;
		border-radius: 999px;
		background: #eef2ff;
		color: #4338ca;
		font-size: 0.72rem;
	}

	.card-desc {
		margin: 0;
		color: #3f3f46;
		font-size: 0.88rem;
		line-height: 1.5;
	}

	.card-open {
		margin-top: auto;
		padding-top: 0.35rem;
		color: #4338ca;
		font-size: 0.8rem;
		font-weight: 500;
	}
</style>
