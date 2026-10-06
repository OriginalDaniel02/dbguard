// Core logic of the DB Guard extension. It deliberately does not import "vscode",
// so it can be unit tested and used from the real-CLI integration test.

export type Risk = 'safe' | 'low' | 'medium' | 'medium-high' | 'high';

export interface Estimate {
	min_seconds: number;
	max_seconds: number;
	text: string;
}

/** One finding in `dbguard check --format json` (see docs/json.md). */
export interface Finding {
	rule: string;
	risk: Risk;
	table: string;
	rows: number;
	line: number; // 1-based
	statement: string;
	lock: string;
	message: string;
	alternative: string;
	estimate?: Estimate;
	override?: string;
	blocking: boolean;
}

export interface FileReport {
	path: string;
	findings: Finding[];
	problems: string[];
}

export type Severity = 'error' | 'warning' | 'info' | 'hint';

export interface DiagnosticModel {
	/** 0-based first line. */
	startLine: number;
	/** 0-based last line (inclusive). */
	endLine: number;
	severity: Severity;
	message: string;
	/** The rule id for findings; "not-analyzed" for problems. */
	code: string;
	acknowledged: boolean;
	kind: 'finding' | 'problem';
	rule?: string;
}

export class ReportError extends Error {}

/** Parses the stdout of `dbguard check --format json`. */
export function parseReport(stdout: string): FileReport[] {
	let data: unknown;
	try {
		data = JSON.parse(stdout);
	} catch {
		throw new ReportError('dbguard did not print JSON. Is it version 0.4.0 or newer?');
	}
	if (!Array.isArray(data)) {
		throw new ReportError('unexpected dbguard output: expected a JSON array');
	}
	return data.map((f: any): FileReport => ({
		path: String(f?.path ?? ''),
		findings: Array.isArray(f?.findings) ? f.findings : [],
		problems: Array.isArray(f?.problems) ? f.problems.map(String) : [],
	}));
}

/** Risk level to severity. A blocking or acknowledged finding is decided by dbguard itself. */
export function severityFor(f: Pick<Finding, 'risk' | 'blocking' | 'override'>): Severity {
	if (f.override) {
		return 'hint';
	}
	if (f.blocking) {
		return 'error';
	}
	switch (f.risk) {
		case 'high':
		case 'medium-high':
		case 'medium':
			return 'warning';
		default:
			return 'info';
	}
}

/** The message shown for a finding: what happens, what it locks, and the safer way. */
export function messageFor(f: Finding): string {
	const lines = [f.message];
	if (f.lock && f.lock !== f.message) {
		lines.push(f.lock);
	}
	if (f.override) {
		lines.push(`Acknowledged: ${f.override}`);
	} else {
		lines.push(`Safer: ${f.alternative}`);
	}
	return lines.join('\n');
}

/** Converts a file report to diagnostics. Lines in the result are 0-based. */
export function toDiagnostics(report: FileReport): DiagnosticModel[] {
	const out: DiagnosticModel[] = [];
	for (const f of report.findings) {
		const start = Math.max(0, f.line - 1);
		const extra = (f.statement.match(/\n/g) ?? []).length;
		out.push({
			startLine: start,
			endLine: start + extra,
			severity: severityFor(f),
			message: messageFor(f),
			code: f.rule,
			rule: f.rule,
			acknowledged: !!f.override,
			kind: 'finding',
		});
	}
	for (const p of report.problems) {
		const m = /^line (\d+):\s*(.*)$/s.exec(p);
		const line = m ? Math.max(0, Number(m[1]) - 1) : 0;
		out.push({
			startLine: line,
			endLine: line,
			severity: 'info',
			message: m ? m[2] : p,
			code: 'not-analyzed',
			acknowledged: false,
			kind: 'problem',
		});
	}
	return out.sort((a, b) => a.startLine - b.startLine);
}

export interface Settings {
	engine: string;
	dsnEnv: string;
	tableRows: Record<string, number>;
	largeRows: number;
	failOn: string;
	mysqlVersion: string;
	placeholders: Record<string, string>;
}

/** The arguments for `dbguard check` on one file. */
export function buildArgs(s: Settings, file: string): string[] {
	const args = ['check', '--format', 'json', '--fail-on', s.failOn, '--large-rows', String(s.largeRows)];
	if (s.engine) {
		args.push('--engine', s.engine);
	}
	if (s.dsnEnv && s.dsnEnv !== 'DBGUARD_DSN') {
		args.push('--dsn-env', s.dsnEnv);
	}
	if (s.mysqlVersion) {
		args.push('--mysql-version', s.mysqlVersion);
	}
	for (const [table, rows] of Object.entries(s.tableRows)) {
		args.push('--rows', `${table}=${rows}`);
	}
	for (const [name, value] of Object.entries(s.placeholders)) {
		args.push('--placeholder', `${name}=${value}`);
	}
	args.push(file);
	return args;
}

/** True for Flyway-named migrations: SQL (V1__x.sql, V2.1__x.sql, R__x.sql) and Java (V2__Add_index.java). */
export function isFlywayName(basename: string): boolean {
	return /^(V[0-9][0-9._]*|R)__.+\.(sql|java)$/.test(basename);
}

export type CommentStyle = 'sql' | 'xml' | 'yaml' | 'java';

/** The comment syntax for acknowledging a risk in a file, or undefined (JSON has no comments). */
export function commentStyleFor(fileName: string): CommentStyle | undefined {
	const ext = fileName.toLowerCase().split('.').pop() ?? '';
	switch (ext) {
		case 'sql':
			return 'sql';
		case 'xml':
			return 'xml';
		case 'yaml':
		case 'yml':
			return 'yaml';
		case 'java':
			return 'java';
		default:
			return undefined;
	}
}

/**
 * The ignore comment for a rule. The reason is mandatory and is sanitized for the
 * comment syntax (an XML comment cannot contain "--").
 */
export function ignoreComment(style: CommentStyle, rule: string, reason: string): string {
	const clean = reason.replace(/\s+/g, ' ').trim();
	if (!clean) {
		throw new Error('a reason is required');
	}
	switch (style) {
		case 'sql':
			return `-- dbguard:ignore ${rule} reason: ${clean}`;
		case 'xml':
			return `<!-- dbguard:ignore ${rule} reason: ${clean.replace(/-{2,}/g, '-')} -->`;
		case 'yaml':
			return `# dbguard:ignore ${rule} reason: ${clean}`;
		case 'java':
			return `// dbguard:ignore ${rule} reason: ${clean}`;
	}
}

/** Parses "dbguard v0.3.0" or "dbguard dev". Returns undefined for a dev build. */
export function parseVersion(output: string): [number, number, number] | undefined {
	const m = /v?(\d+)\.(\d+)\.(\d+)/.exec(output);
	return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : undefined;
}

// 0.4.0 added Java migrations: an older dbguard would fail on a .java file instead of analyzing it.
export const MIN_VERSION: [number, number, number] = [0, 4, 0];

/** True if the binary is new enough (a dev build is accepted). */
export function versionOk(output: string): boolean {
	const v = parseVersion(output);
	if (!v) {
		return /dev/.test(output);
	}
	for (let i = 0; i < 3; i++) {
		if (v[i] !== MIN_VERSION[i]) {
			return v[i] > MIN_VERSION[i];
		}
	}
	return true;
}

/** One-line summary for the status bar: counts of findings that are not acknowledged. */
export function summarize(diags: DiagnosticModel[]): { errors: number; warnings: number; text: string } {
	const live = diags.filter((d) => d.kind === 'finding' && !d.acknowledged);
	const errors = live.filter((d) => d.severity === 'error').length;
	const warnings = live.length - errors;
	if (live.length === 0) {
		return { errors, warnings, text: 'no risks' };
	}
	const parts: string[] = [];
	if (errors) {
		parts.push(`${errors} blocking`);
	}
	if (warnings) {
		parts.push(`${warnings} to review`);
	}
	return { errors, warnings, text: parts.join(', ') };
}
