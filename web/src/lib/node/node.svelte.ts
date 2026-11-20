import { api, Code, codeOf } from '#lib/api/client.js';
import type { Filesystem, Node } from '#lib/api/gen/sermon/web/v1/node_pb.js';

/** Refresh of the node header and the file systems. */
export const NODE_INTERVAL = 15_000;

/** One node: details and file systems, refreshed while the page is open. */
export class NodeState {
	node = $state<Node | undefined>(undefined);
	filesystems = $state<Filesystem[]>([]);
	status = $state<'loading' | 'ready' | 'not_found' | 'error'>('loading');
	updatedAt = $state(Date.now());

	constructor(readonly id: string) {}

	async load(): Promise<void> {
		try {
			const [node, filesystems] = await Promise.all([
				api.node.getNode({ nodeId: this.id }),
				api.node.listFilesystems({ nodeId: this.id })
			]);
			this.node = node.node;
			this.filesystems = filesystems.filesystems;
			this.status = 'ready';
			this.updatedAt = Date.now();
		} catch (error) {
			const code = codeOf(error);
			if (code === Code.Unauthenticated) return;
			// Foreign and unknown nodes look the same (NOT_FOUND), malformed IDs too.
			if (code === Code.NotFound || code === Code.InvalidArgument) this.status = 'not_found';
			else if (!this.node) this.status = 'error';
		}
	}

	start(): () => void {
		void this.load();
		const timer = setInterval(() => void this.load(), NODE_INTERVAL);
		return () => clearInterval(timer);
	}
}
