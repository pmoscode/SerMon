/**
 * How long a downloaded file's blob URL stays valid: revoked right after the click, some browsers
 * cancel the download (M2-39).
 */
export const REVOKE_AFTER_MS = 60_000;

/** Offers data as a file to save (exports, audit logs, recovery codes); nothing leaves the page. */
export function saveFile(data: BlobPart, name: string, type: string): void {
	const url = URL.createObjectURL(new Blob([data], { type }));
	const link = document.createElement('a');
	link.href = url;
	link.download = name;
	link.click();
	setTimeout(() => URL.revokeObjectURL(url), REVOKE_AFTER_MS);
}
