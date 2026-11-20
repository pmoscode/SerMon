<script lang="ts">
	import { encode } from 'uqr';

	/** A QR code as SVG drawn from the module matrix (no HTML injection, no inline styles). */
	let { value, label, size = 192 }: { value: string; label: string; size?: number } = $props();

	const qr = $derived(encode(value, { ecc: 'M', border: 2 }));
	const path = $derived(
		qr.data.flatMap((row, y) => row.map((dark, x) => (dark ? `M${x} ${y}h1v1h-1z` : ''))).join('')
	);
</script>

<svg
	role="img"
	aria-label={label}
	width={size}
	height={size}
	viewBox="0 0 {qr.size} {qr.size}"
	shape-rendering="crispEdges"
	class="rounded-md"
>
	<rect width={qr.size} height={qr.size} fill="#fff" />
	<path d={path} fill="#000" />
</svg>
