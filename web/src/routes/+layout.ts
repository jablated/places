// The app is a static bundle served by the Go binary and talks to /api at
// runtime, so there is nothing to render on a server: prerender the shell and
// turn SSR off. This is what lets adapter-static emit a single index.html.
export const prerender = true;
export const ssr = false;
