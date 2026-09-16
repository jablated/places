// Small display helpers shared by the list, trips and trip-detail views.

/** Strips the lightest markdown so a preview reads as prose, not source. */
export function preview(markdown: string, limit = 220): string {
	const flat = markdown
		.replace(/!\[[^\]]*\]\([^)]*\)/g, '') // images
		.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1') // links → label
		.replace(/[*_`#>]/g, '')
		.replace(/\s+/g, ' ')
		.trim();
	return flat.length > limit ? `${flat.slice(0, limit).trimEnd()}…` : flat;
}

export function formatDate(iso: string): string {
	const d = new Date(iso);
	return Number.isNaN(d.getTime())
		? ''
		: d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

/** Shows "example.com" rather than a 90-character URL. */
export function hostOf(url: string): string {
	try {
		return new URL(url).hostname.replace(/^www\./, '');
	} catch {
		return url;
	}
}

/** "1 stop" / "4 stops" — the pluralisation the trip views repeat everywhere. */
export function plural(n: number, one: string, many = `${one}s`): string {
	return `${n} ${n === 1 ? one : many}`;
}
