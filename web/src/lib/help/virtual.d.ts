// The full texts of a language: in a build a JSON file fetched when first needed (scripts/help.js).
declare module 'virtual:sermon-help/de' {
	const docs: Promise<Record<string, import('./types.js').HelpDoc>>;
	export default docs;
}
declare module 'virtual:sermon-help/en' {
	const docs: Promise<Record<string, import('./types.js').HelpDoc>>;
	export default docs;
}
// The titles of a language (virtual:sermon-help/<locale>/titles), part of the code.
declare module 'virtual:sermon-help/*' {
	const docs: Record<string, import('./types.js').HelpDoc>;
	export default docs;
}
