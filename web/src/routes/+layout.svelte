<script lang="ts">
	import favicon from '$lib/assets/favicon.svg';

	let { children } = $props();
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
</svelte:head>

{@render children()}

<style>
	:global(*, *::before, *::after) {
		box-sizing: border-box;
	}

	:global(html, body) {
		height: 100%;
		margin: 0;
	}

	:global(body) {
		font-family:
			system-ui,
			-apple-system,
			'Segoe UI',
			sans-serif;
		color: #18181b;
		background: #fafafa;
		-webkit-font-smoothing: antialiased;
	}

	/* SvelteKit wraps the app in a display:contents wrapper. Leaflet needs a
	   real height to size its canvas, so keep the chain intact in case that
	   wrapper ever starts generating a box. */
	:global(body > div) {
		height: 100%;
	}
</style>
