/** Light, dark or following the operating system (KONZEPT §17). The choice is kept in localStorage. */
export type Theme = 'light' | 'dark' | 'system';

export const themes: readonly Theme[] = ['light', 'dark', 'system'];

/** Must match the bootstrap script in app.html, which applies the theme before the first paint. */
export const THEME_KEY = 'sermon-theme';

function stored(): Theme {
	try {
		const value = localStorage.getItem(THEME_KEY);
		return themes.includes(value as Theme) ? (value as Theme) : 'system';
	} catch {
		return 'system';
	}
}

const darkQuery = () => window.matchMedia('(prefers-color-scheme: dark)');

/** Whether a theme shows dark colors right now. */
export function isDark(theme: Theme, systemDark: boolean): boolean {
	return theme === 'dark' || (theme === 'system' && systemDark);
}

class ThemeState {
	current = $state<Theme>(stored());
	systemDark = $state(darkQuery().matches);

	constructor() {
		darkQuery().addEventListener('change', (event) => {
			this.systemDark = event.matches;
			this.apply();
		});
	}

	/** Chooses a theme, remembers it and applies it at once. */
	set(theme: Theme): void {
		this.current = theme;
		try {
			localStorage.setItem(THEME_KEY, theme);
		} catch {
			// Storage blocked (private mode): the choice lasts for this page only.
		}
		this.apply();
	}

	apply(): void {
		document.documentElement.classList.toggle('dark', isDark(this.current, this.systemDark));
	}
}

export const themeState = new ThemeState();
