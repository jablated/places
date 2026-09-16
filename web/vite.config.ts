import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// Where `make dev-api` listens. The dev server proxies /api to it so the
// frontend uses the same relative URLs in dev as it does in the shipped binary
// — no VITE_API_BASE, no CORS, no drift between the two setups.
const API_TARGET = process.env.VITE_API_TARGET ?? 'http://localhost:8080';

export default defineConfig({
	server: {
		proxy: {
			'/api': { target: API_TARGET, changeOrigin: true },
			'/health': { target: API_TARGET, changeOrigin: true }
		}
	},

	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},

			// adapter-static: the whole app is prerendered to plain files that the
			// Go binary embeds and serves. No `fallback` is configured because the
			// Go server already falls back to index.html for unknown paths — asking
			// the adapter for one too just overwrites the prerendered index.html
			// with an identical file and logs a warning.
			adapter: adapter({
				pages: 'build',
				assets: 'build',
				precompress: false,
				strict: true
			}),

			// Served from the domain root by the Go binary, so no base prefix.
			paths: {
				base: ''
			}
		})
	]
});
