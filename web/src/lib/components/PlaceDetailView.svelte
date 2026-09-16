<script lang="ts">
	import { api, ApiError, type Place } from '$lib/api';
	import { hostOf } from '$lib/format';

	let { placeId, onBack }: { placeId: string; onBack: () => void } = $props();

	let place = $state<Place | null>(null);
	let loading = $state(true);
	let error = $state('');
	let retryTick = $state(0);

	// Keyed on placeId rather than onMount, so opening a different place while
	// this panel is up refetches, and a slow earlier response is discarded.
	$effect(() => {
		const id = placeId;
		void retryTick;
		const ctrl = new AbortController();

		place = null;
		loading = true;
		error = '';

		api
			.getPlace(id, ctrl.signal)
			.then((p) => {
				if (!ctrl.signal.aborted) place = p;
			})
			.catch((err) => {
				if (ctrl.signal.aborted) return;
				error = err instanceof ApiError ? err.message : 'failed to load the place';
			})
			.finally(() => {
				if (!ctrl.signal.aborted) loading = false;
			});

		return () => ctrl.abort();
	});

	const locality = $derived(
		place ? [place.neighborhood, place.city].filter(Boolean).join(' · ') : ''
	);
	// Blank-line separated blocks become paragraphs; single newlines inside a
	// block are kept by pre-line.
	const paragraphs = $derived(
		place?.description
			? place.description
					.split(/\n\s*\n/)
					.map((s) => s.trim())
					.filter(Boolean)
			: []
	);
</script>

<div class="place-scroll">
	<div class="place">
		<button type="button" class="back" onclick={onBack}>← Back</button>

		{#if loading}
			<p class="notice">Loading place…</p>
		{:else if error}
			<div class="notice notice--error">
				<p>{error}</p>
				<button type="button" onclick={() => retryTick++}>Retry</button>
			</div>
		{:else if place}
			<header class="head">
				<h1 class="title">{place.name}</h1>

				{#if place.visited}
					<span class="badge-visited">✓ visited</span>
				{/if}

				{#if locality}
					<p class="meta">{locality}</p>
				{/if}

				{#if place.tags.length}
					<p class="tags">
						{#each place.tags as tag (tag)}
							<span class="tag">{tag}</span>
						{/each}
					</p>
				{/if}
			</header>

			{#if paragraphs.length}
				<div class="desc">
					{#each paragraphs as para, i (i)}
						<p>{para}</p>
					{/each}
				</div>
			{/if}

			<div class="links">
				{#if place.source_url}
					<a class="source" href={place.source_url} target="_blank" rel="noopener noreferrer">
						{hostOf(place.source_url)} ↗
					</a>
				{/if}

				{#if place.lat != null && place.lng != null}
					<a
						class="nav"
						href="https://www.google.com/maps/dir/?api=1&destination={place.lat},{place.lng}"
						target="_blank"
						rel="noopener noreferrer">Navigate in Google Maps ↗</a
					>
				{/if}
			</div>
		{/if}
	</div>
</div>

<style>
	/* Fills the content pane and scrolls on its own, since the map layout sets
	   the pane to overflow: hidden. */
	.place-scroll {
		height: 100%;
		overflow-y: auto;
	}

	.place {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.9rem;
		max-width: 760px;
		margin: 0 auto;
		padding: 1.25rem 1.25rem 3rem;
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

	.head {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.45rem;
	}

	.title {
		margin: 0;
		font-size: 1.6rem;
		line-height: 1.2;
	}

	.badge-visited {
		padding: 0.1rem 0.5rem;
		border-radius: 999px;
		background: #dcfce7;
		color: #15803d;
		font-size: 0.72rem;
		font-weight: 500;
	}

	.meta {
		margin: 0;
		color: #71717a;
		font-size: 0.85rem;
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
		max-width: 65ch;
		color: #3f3f46;
		font-size: 0.95rem;
		line-height: 1.65;
	}

	.desc p {
		margin: 0 0 0.9rem;
		white-space: pre-line;
		overflow-wrap: anywhere;
	}

	.desc p:last-child {
		margin-bottom: 0;
	}

	.links {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.5rem;
	}

	.source {
		color: #4338ca;
		font-size: 0.82rem;
		text-decoration: none;
		overflow-wrap: anywhere;
	}

	.nav {
		color: #059669;
		font-size: 0.92rem;
		font-weight: 500;
		text-decoration: none;
	}

	.source:hover,
	.nav:hover {
		text-decoration: underline;
	}

	.notice {
		margin: 1rem 0 0;
		color: #71717a;
	}

	.notice p {
		margin: 0 0 0.6rem;
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

	@media (max-width: 600px) {
		.place {
			padding-inline: 0.9rem;
		}

		.title {
			font-size: 1.35rem;
		}
	}
</style>
