<script lang="ts">
	import { saveFile } from '#lib/download/download.js';
	import Copy from '@lucide/svelte/icons/copy';
	import Download from '@lucide/svelte/icons/download';
	import { Button } from '#lib/components/ui/button/index.js';
	import { m } from '#lib/paraglide/messages.js';

	/** Shows new recovery codes once, with copy and download (as a text file, never sent anywhere). */
	let { codes, onDone }: { codes: string[]; onDone: () => void } = $props();

	let copied = $state(false);
	const text = $derived(codes.join('\n') + '\n');

	async function copy() {
		await navigator.clipboard.writeText(text);
		copied = true;
	}

	function download() {
		saveFile(text, 'sermon-recovery-codes.txt', 'text/plain');
	}
</script>

<section class="flex flex-col gap-3" aria-labelledby="recovery-codes-title">
	<h2 id="recovery-codes-title" class="text-lg font-medium">{m.security_codes_title()}</h2>
	<p role="alert" class="rounded-md border border-amber-500/50 p-3 text-sm">
		{m.security_codes_once()}
	</p>
	<ol
		class="grid grid-cols-1 gap-1 rounded-md border bg-muted p-3 font-mono text-sm sm:grid-cols-2"
	>
		{#each codes as code (code)}
			<li>{code}</li>
		{/each}
	</ol>
	<div class="flex flex-wrap gap-2">
		<Button variant="outline" onclick={copy}>
			<Copy aria-hidden="true" />
			{copied ? m.security_codes_copied() : m.security_codes_copy()}
		</Button>
		<Button variant="outline" onclick={download}>
			<Download aria-hidden="true" />
			{m.security_codes_download()}
		</Button>
		<Button onclick={onDone}>{m.security_codes_done()}</Button>
	</div>
</section>
