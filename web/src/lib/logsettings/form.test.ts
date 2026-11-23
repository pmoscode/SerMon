import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { LogSettingsSchema, LogSeverity } from '#lib/api/gen/sermon/web/v1/log_settings_pb.js';
import {
	MAX_FILES,
	MAX_UNITS,
	draftOf,
	globProblem,
	problems,
	ruleProblem,
	settingsOf,
	unitsOf,
	type GroupDraft
} from './form.js';

const draft = (over: Partial<GroupDraft> = {}): GroupDraft => ({
	enabled: true,
	minSeverity: '',
	excludeUnits: '',
	files: [],
	...over
});

describe('globProblem', () => {
	it('accepts absolute paths with * and ?', () => {
		for (const p of ['/var/log/nginx/*.log', '/var/log/*/error.log', '/srv/app/app-?.log', '/x'])
			expect(globProblem(p), p).toBeUndefined();
	});

	it('names what the agent would refuse', () => {
		expect(globProblem('')).toBe('empty');
		expect(globProblem('var/log/x')).toBe('absolute');
		expect(globProblem('/var/log/../../etc/shadow')).toBe('segments');
		expect(globProblem('/var/log/./x')).toBe('segments');
		expect(globProblem('/var/log//x')).toBe('segments');
		expect(globProblem('/var/log/')).toBe('segments');
		expect(globProblem('/var/log/[ab].log')).toBe('wildcards');
		expect(globProblem('/var/log/a\\*.log')).toBe('wildcards');
		expect(globProblem('/' + 'x'.repeat(1024))).toBe('long');
		expect(globProblem('/var/log/a\u0000b')).toBe('absolute');
	});
});

describe('unitsOf', () => {
	it('splits by commas and lines, drops duplicates', () => {
		expect(unitsOf('cron.service, nginx.service\n\ncron.service ,user@1000.service')).toEqual({
			units: ['cron.service', 'nginx.service', 'user@1000.service']
		});
	});

	it('refuses names that are no units and too many', () => {
		expect(unitsOf('nginx service').problem).toBe('unit');
		expect(unitsOf('a/b').problem).toBe('unit');
		const many = Array.from({ length: MAX_UNITS + 1 }, (_, i) => `u${i}`).join(',');
		expect(unitsOf(many).problem).toBe('units');
	});
});

describe('ruleProblem', () => {
	it('checks only what the browser can judge', () => {
		expect(ruleProblem('customer-\\d{6}')).toBeUndefined();
		// RE2 syntax the browser doesn't know is the server's to judge.
		expect(ruleProblem('(?i)pin\\s*(?P<secret>\\d{4})')).toBeUndefined();
		expect(ruleProblem('')).toBe('empty');
		expect(ruleProblem('a'.repeat(1025))).toBe('long');
	});
});

describe('draft ↔ settings', () => {
	it('round-trips the settings', () => {
		const settings = create(LogSettingsSchema, {
			enabled: true,
			minSeverity: LogSeverity.WARNING,
			excludeUnits: ['cron.service', 'snapd.service'],
			files: [{ pattern: '/var/log/app/*.log', multilineStart: '^\\d{4}-' }]
		});
		const d = draftOf(settings);
		expect(d).toEqual({
			enabled: true,
			minSeverity: 'warning',
			excludeUnits: 'cron.service\nsnapd.service',
			files: [{ pattern: '/var/log/app/*.log', multiline: '^\\d{4}-' }]
		});
		expect(settingsOf(d)).toEqual({
			enabled: true,
			minSeverity: LogSeverity.WARNING,
			excludeUnits: ['cron.service', 'snapd.service'],
			files: [{ pattern: '/var/log/app/*.log', multilineStart: '^\\d{4}-' }]
		});
	});

	it('trims patterns and drops empty file rows', () => {
		const s = settingsOf(
			draft({
				files: [
					{ pattern: ' /var/log/a.log ', multiline: ' ' },
					{ pattern: '', multiline: '' }
				]
			})
		);
		expect(s.files).toEqual([{ pattern: '/var/log/a.log', multilineStart: '' }]);
	});
});

describe('problems', () => {
	it('is empty for a good draft', () => {
		expect(problems(draft({ files: [{ pattern: '/var/log/a.log', multiline: '' }] }))).toEqual({
			files: [undefined]
		});
	});

	it('collects the problems of files, units and duplicates', () => {
		const p = problems(
			draft({
				excludeUnits: 'bad unit',
				files: [
					{ pattern: '/var/log/a.log', multiline: '' },
					{ pattern: '/var/log/a.log', multiline: '' },
					{ pattern: 'relative', multiline: '' },
					{ pattern: '/var/log/b.log', multiline: 'x'.repeat(1025) }
				]
			})
		);
		expect(p).toEqual({
			units: 'unit',
			files: [undefined, 'duplicate', 'absolute', 'multiline']
		});
		const many = Array.from({ length: MAX_FILES + 1 }, (_, i) => ({
			pattern: `/var/log/f${i}.log`,
			multiline: ''
		}));
		expect(problems(draft({ files: many })).tooManyFiles).toBe(true);
	});
});
