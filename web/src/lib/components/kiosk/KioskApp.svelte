<script lang="ts">
	import KioskView from './KioskView.svelte';
	// Not used here, but loaded by every other page: with the same shared modules the bundler keeps
	// the shared chunks as they are. Without these the kiosk split them and every page grew by
	// 7 KiB of gzip (M5-27). The display's calls go through kioskClient only, never the session.
	import '#lib/api/client.js';
	import '#lib/components/ui/button/index.js';
	import { presetOf } from '#lib/dashboards/model.js';
	import { kioskClient } from '#lib/kiosk/api.js';
	import { parseFragment } from '#lib/kiosk/fragment.js';
	import { KioskState, REFRESH_MS } from '#lib/kiosk/kiosk.svelte.js';
	import { m } from '#lib/paraglide/messages.js';
	import { timeRange } from '#lib/stores/timerange.svelte.js';

	/**
	 * The kiosk display (KONZEPT §16, M5-27): `/kiosk#t=<token>,…&rotate=60`, no session. Every
	 * 30 s the dashboards and their widgets are read again – also the reconnect after an outage –,
	 * several dashboards take turns.
	 */
	const config = parseFragment(location.hash);
	const kiosk = new KioskState(config.tokens, { client: kioskClient });

	void kiosk.refresh();
	$effect(() => {
		const timer = setInterval(() => {
			void kiosk.refresh();
			timeRange.refresh();
		}, REFRESH_MS);
		const online = () => void kiosk.refresh();
		// A changed address names other tokens: start anew.
		const changed = () => location.reload();
		window.addEventListener('online', online);
		window.addEventListener('hashchange', changed);
		return () => {
			clearInterval(timer);
			window.removeEventListener('online', online);
			window.removeEventListener('hashchange', changed);
		};
	});
	$effect(() => {
		const timer = setInterval(() => kiosk.rotate(), config.rotate * 1000);
		return () => clearInterval(timer);
	});
	// The charts show the current dashboard's default range.
	const preset = $derived(kiosk.current?.spec ? presetOf(kiosk.current.spec.timeRange) : undefined);
	$effect(() => {
		if (preset) timeRange.setPreset(preset);
	});
</script>

<svelte:head>
	<title>{kiosk.current?.spec?.title ?? m.kiosk_title()} · SerMon</title>
</svelte:head>

<KioskView {kiosk} configured={config.tokens.length > 0} />
