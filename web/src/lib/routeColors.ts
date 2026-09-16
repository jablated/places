// Qualitative palette for trip route segments. Hues are spread far enough apart
// to stay tellable-apart as 18px dots on a pale basemap, and the first two are
// the accent and pin colors the rest of the app already uses.
//
// Colors are assigned by a route's position in the trip, not by its label, so a
// trip's segments read as "1, 2, 3" and the legend beside each route heading
// matches its pins.
export const ROUTE_COLORS = [
	'#4338ca', // indigo
	'#e5484d', // red
	'#0e9f6e', // green
	'#d97706', // amber
	'#0891b2', // cyan
	'#be185d', // magenta
	'#65a30d', // lime
	'#7c3aed' // violet
];

/** Wraps for trips with more segments than the palette has colors. */
export function routeColor(index: number): string {
	return ROUTE_COLORS[((index % ROUTE_COLORS.length) + ROUTE_COLORS.length) % ROUTE_COLORS.length];
}
