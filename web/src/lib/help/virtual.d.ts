declare module 'virtual:sermon-help/*' {
	const docs: Record<string, import('./types.js').HelpDoc>;
	export default docs;
}
