// Small spherical-earth helpers for the Near Me view. Accuracy is well within
// what a city-scale "what's around me" list needs.

export interface BBox {
	swLat: number;
	swLng: number;
	neLat: number;
	neLng: number;
}

const EARTH_RADIUS_MI = 3958.8;

const toRad = (deg: number) => (deg * Math.PI) / 180;
const toDeg = (rad: number) => (rad * 180) / Math.PI;

/** Great-circle distance between two points, in miles. */
export function haversineDistanceMi(lat1: number, lng1: number, lat2: number, lng2: number): number {
	const dLat = toRad(lat2 - lat1);
	const dLng = toRad(lng2 - lng1);
	const a =
		Math.sin(dLat / 2) ** 2 + Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLng / 2) ** 2;
	return 2 * EARTH_RADIUS_MI * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
}

/** The smallest box containing a circle of `radiusMi` around a point. */
export function bboxFromRadius(lat: number, lng: number, radiusMi: number): BBox {
	const dLat = toDeg(radiusMi / EARTH_RADIUS_MI);
	// A degree of longitude shrinks with latitude, so the box widens to match.
	const dLng = dLat / Math.cos(toRad(lat));
	return { swLat: lat - dLat, swLng: lng - dLng, neLat: lat + dLat, neLng: lng + dLng };
}

/** True when `inner` lies entirely within `outer`. */
export function containsBBox(outer: BBox, inner: BBox): boolean {
	return (
		inner.swLat >= outer.swLat &&
		inner.swLng >= outer.swLng &&
		inner.neLat <= outer.neLat &&
		inner.neLng <= outer.neLng
	);
}

export function placeInBBox(lat: number, lng: number, bbox: BBox): boolean {
	return lat >= bbox.swLat && lat <= bbox.neLat && lng >= bbox.swLng && lng <= bbox.neLng;
}

/** Scales a box about its center; factor 2 doubles its width and height. */
export function expandBBox(bbox: BBox, factor: number): BBox {
	const cLat = (bbox.swLat + bbox.neLat) / 2;
	const cLng = (bbox.swLng + bbox.neLng) / 2;
	const hLat = ((bbox.neLat - bbox.swLat) / 2) * factor;
	const hLng = ((bbox.neLng - bbox.swLng) / 2) * factor;
	return { swLat: cLat - hLat, swLng: cLng - hLng, neLat: cLat + hLat, neLng: cLng + hLng };
}
