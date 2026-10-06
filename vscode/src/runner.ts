// Runs the dbguard CLI. No "vscode" import: used by the extension and by the integration test.

import { execFile, ChildProcess } from 'child_process';
import { FileReport, ReportError, parseReport, versionOk } from './core';

export interface RunResult {
	/** dbguard's exit code: 0 nothing blocks, 1 something blocks, 2 error. */
	code: number;
	reports: FileReport[];
	stderr: string;
}

export class BinaryNotFound extends Error {
	constructor(readonly binary: string) {
		super(`could not run "${binary}"`);
	}
}

export interface Handle {
	child: ChildProcess;
	result: Promise<RunResult>;
}

/** Starts `dbguard check` and returns a handle so a newer request can cancel it. */
export function runCheck(binary: string, args: string[], cwd: string | undefined, env: NodeJS.ProcessEnv = process.env): Handle {
	let child!: ChildProcess;
	const result = new Promise<RunResult>((resolve, reject) => {
		child = execFile(
			binary,
			args,
			{ cwd, env, timeout: 60_000, maxBuffer: 32 * 1024 * 1024, windowsHide: true },
			(err, stdout, stderr) => {
				const failure = err as (NodeJS.ErrnoException & { code?: string | number; killed?: boolean }) | null;
				if (failure && failure.code === 'ENOENT') {
					return reject(new BinaryNotFound(binary));
				}
				if (failure && failure.killed) {
					return reject(new Error('dbguard was cancelled or timed out'));
				}
				const code = failure ? (typeof failure.code === 'number' ? failure.code : 2) : 0;
				if (code === 2) {
					return resolve({ code, reports: [], stderr: String(stderr).trim() || String(stdout).trim() });
				}
				try {
					resolve({ code, reports: parseReport(String(stdout)), stderr: String(stderr).trim() });
				} catch (e) {
					reject(e instanceof ReportError ? e : new ReportError(String(e)));
				}
			},
		);
	});
	return { child, result };
}

/** Runs `dbguard version` and reports whether the binary is new enough. */
export function checkVersion(binary: string): Promise<{ ok: boolean; output: string }> {
	return new Promise((resolve, reject) => {
		execFile(binary, ['version'], { timeout: 15_000, windowsHide: true }, (err, stdout) => {
			const failure = err as NodeJS.ErrnoException | null;
			if (failure && failure.code === 'ENOENT') {
				return reject(new BinaryNotFound(binary));
			}
			if (failure) {
				// Versions before 0.3.0 have no `version` command: they print usage and exit 2.
				return resolve({ ok: false, output: 'an older version' });
			}
			const output = String(stdout).trim();
			resolve({ ok: versionOk(output), output });
		});
	});
}

/** Runs `dbguard changelog` and returns its text output. */
export function runChangelog(binary: string, args: string[], cwd: string | undefined, env: NodeJS.ProcessEnv = process.env): Promise<string> {
	return new Promise((resolve, reject) => {
		execFile(binary, ['changelog', ...args], { cwd, env, timeout: 60_000, maxBuffer: 32 * 1024 * 1024, windowsHide: true }, (err, stdout, stderr) => {
			const failure = err as NodeJS.ErrnoException | null;
			if (failure && failure.code === 'ENOENT') {
				return reject(new BinaryNotFound(binary));
			}
			if (failure) {
				return reject(new Error(String(stderr).trim() || String(failure.message)));
			}
			resolve(String(stdout));
		});
	});
}
