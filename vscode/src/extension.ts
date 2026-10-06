import * as vscode from 'vscode';
import * as crypto from 'crypto';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import {
	DiagnosticModel,
	MIN_VERSION,
	Settings,
	buildArgs,
	commentStyleFor,
	ignoreComment,
	isFlywayName,
	summarize,
	toDiagnostics,
} from './core';
import { BinaryNotFound, Handle, checkVersion, runChangelog, runCheck } from './runner';

const SOURCE = 'DB Guard';
const LANGUAGES = ['sql', 'xml', 'yaml', 'json'];
const DOCS = vscode.Uri.parse('https://github.com/OriginalDaniel02/dbguard#risk-rules');
const RELEASES = vscode.Uri.parse('https://github.com/OriginalDaniel02/dbguard/releases');

interface DocState {
	seq: number;
	handle?: Handle;
	timer?: NodeJS.Timeout;
	models: DiagnosticModel[];
}

/** What tests (and other extensions) can call. */
export interface Api {
	/** Checks a document now and resolves when its diagnostics are up to date. */
	check(doc: vscode.TextDocument, explicit?: boolean): Promise<void>;
}

export function activate(context: vscode.ExtensionContext): Api {
	const diagnostics = vscode.languages.createDiagnosticCollection('dbguard');
	const output = vscode.window.createOutputChannel('DB Guard');
	const status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 50);
	status.command = 'dbguard.checkFile';
	const states = new Map<string, DocState>();
	let versionVerdict: Promise<boolean> | undefined;
	let warnedMissing = false;
	let tmpRoot: string | undefined;

	context.subscriptions.push(diagnostics, output, status);

	const cfg = () => vscode.workspace.getConfiguration('dbguard');

	function settings(): Settings {
		const c = cfg();
		return {
			engine: c.get<string>('engine', ''),
			dsnEnv: c.get<string>('dsnEnv', 'DBGUARD_DSN'),
			tableRows: c.get<Record<string, number>>('tableRows', {}),
			largeRows: c.get<number>('largeRows', 100000),
			failOn: c.get<string>('failOn', 'medium-high'),
			mysqlVersion: c.get<string>('mysqlVersion', ''),
			placeholders: c.get<Record<string, string>>('placeholders', {}),
		};
	}

	function workspaceFolderFor(uri: vscode.Uri): string | undefined {
		return vscode.workspace.getWorkspaceFolder(uri)?.uri.fsPath ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
	}

	function binary(uri: vscode.Uri): string {
		const p = cfg().get<string>('path', 'dbguard') || 'dbguard';
		// A relative path with a separator ("./bin/dbguard") is relative to the workspace folder.
		if (!path.isAbsolute(p) && /[\\/]/.test(p)) {
			return path.resolve(workspaceFolderFor(uri) ?? process.cwd(), p);
		}
		return p;
	}

	function isMigration(doc: vscode.TextDocument): boolean {
		if (doc.uri.scheme !== 'file' || !LANGUAGES.includes(doc.languageId)) {
			return false;
		}
		if (isFlywayName(path.basename(doc.uri.fsPath))) {
			return true;
		}
		const globs = cfg().get<string[]>('include', []);
		return globs.length > 0 && vscode.languages.match(globs.map((pattern) => ({ pattern })), doc) > 0;
	}

	function stateFor(doc: vscode.TextDocument): DocState {
		const key = doc.uri.toString();
		let s = states.get(key);
		if (!s) {
			s = { seq: 0, models: [] };
			states.set(key, s);
		}
		return s;
	}

	function tempCopy(doc: vscode.TextDocument): string {
		tmpRoot ??= fs.mkdtempSync(path.join(os.tmpdir(), 'dbguard-vscode-'));
		const dir = path.join(tmpRoot, crypto.createHash('sha1').update(doc.uri.toString()).digest('hex').slice(0, 12));
		fs.mkdirSync(dir, { recursive: true });
		const file = path.join(dir, path.basename(doc.uri.fsPath));
		fs.writeFileSync(file, doc.getText());
		return file;
	}

	function versionGate(bin: string): Promise<boolean> {
		versionVerdict ??= checkVersion(bin).then(
			(v) => {
				if (!v.ok) {
					void vscode.window
						.showWarningMessage(
							`DB Guard needs dbguard ${MIN_VERSION.join('.')} or newer (found: ${v.output}).`,
							'Open releases',
						)
						.then((choice) => choice && vscode.env.openExternal(RELEASES));
				}
				return v.ok;
			},
			(e) => {
				reportMissing(e);
				return false;
			},
		);
		return versionVerdict;
	}

	function reportMissing(e: unknown): void {
		const bin = e instanceof BinaryNotFound ? e.binary : 'dbguard';
		output.appendLine(`Could not run "${bin}". Install dbguard (version ${MIN_VERSION.join('.')} or newer) or set "dbguard.path".`);
		if (warnedMissing) {
			return;
		}
		warnedMissing = true;
		void vscode.window
			.showErrorMessage(`DB Guard: could not run "${bin}".`, 'Download', 'Open settings')
			.then((choice) => {
				if (choice === 'Download') {
					void vscode.env.openExternal(RELEASES);
				} else if (choice === 'Open settings') {
					void vscode.commands.executeCommand('workbench.action.openSettings', 'dbguard.path');
				}
			});
	}

	function toVsDiagnostic(doc: vscode.TextDocument, m: DiagnosticModel): vscode.Diagnostic {
		const last = Math.max(0, doc.lineCount - 1);
		const start = Math.min(m.startLine, last);
		const end = Math.min(Math.max(m.endLine, start), last);
		const first = doc.lineAt(start);
		const range = new vscode.Range(start, first.firstNonWhitespaceCharacterIndex, end, doc.lineAt(end).range.end.character);
		const severity = {
			error: vscode.DiagnosticSeverity.Error,
			warning: vscode.DiagnosticSeverity.Warning,
			info: vscode.DiagnosticSeverity.Information,
			hint: vscode.DiagnosticSeverity.Hint,
		}[m.severity];
		const d = new vscode.Diagnostic(range, m.message, severity);
		d.source = SOURCE;
		d.code = m.kind === 'finding' ? { value: m.code, target: DOCS } : m.code;
		return d;
	}

	function refreshStatus(): void {
		const doc = vscode.window.activeTextEditor?.document;
		if (!doc || !LANGUAGES.includes(doc.languageId) || !isMigration(doc)) {
			status.hide();
			return;
		}
		const sum = summarize(states.get(doc.uri.toString())?.models ?? []);
		status.text = sum.errors ? `$(error) DB Guard: ${sum.text}` : sum.warnings ? `$(warning) DB Guard: ${sum.text}` : `$(shield) DB Guard: ${sum.text}`;
		status.tooltip = 'Click to check this migration again';
		status.show();
	}

	async function check(doc: vscode.TextDocument, explicit = false): Promise<void> {
		if (!explicit && !isMigration(doc)) {
			return;
		}
		if (doc.uri.scheme !== 'file') {
			return;
		}
		const st = stateFor(doc);
		st.handle?.child.kill();
		const mySeq = ++st.seq;
		const bin = binary(doc.uri);
		if (!(await versionGate(bin))) {
			return;
		}
		if (mySeq !== st.seq) {
			return;
		}
		const file = doc.isDirty ? tempCopy(doc) : doc.uri.fsPath;
		const args = buildArgs(settings(), file);
		output.appendLine(`$ ${bin} ${args.join(' ')}`);
		status.text = '$(sync~spin) DB Guard';
		status.show();
		const handle = runCheck(bin, args, workspaceFolderFor(doc.uri));
		st.handle = handle;
		try {
			const res = await handle.result;
			if (mySeq !== st.seq) {
				return; // a newer check superseded this one
			}
			if (res.code === 2) {
				output.appendLine(`dbguard failed: ${res.stderr}`);
				diagnostics.set(doc.uri, []);
				st.models = [];
				status.text = '$(error) DB Guard: error (see output)';
				status.tooltip = res.stderr;
				return;
			}
			if (res.stderr) {
				output.appendLine(res.stderr);
			}
			const report = res.reports[0];
			st.models = report ? toDiagnostics(report) : [];
			diagnostics.set(doc.uri, st.models.map((m) => toVsDiagnostic(doc, m)));
			refreshStatus();
		} catch (e) {
			if (mySeq !== st.seq) {
				return;
			}
			if (e instanceof BinaryNotFound) {
				reportMissing(e);
			} else {
				output.appendLine(`dbguard failed: ${e instanceof Error ? e.message : String(e)}`);
			}
			status.text = '$(error) DB Guard';
		}
	}

	function schedule(doc: vscode.TextDocument): void {
		const st = stateFor(doc);
		clearTimeout(st.timer);
		st.timer = setTimeout(() => void check(doc), 800);
	}

	// Events.
	context.subscriptions.push(
		vscode.workspace.onDidOpenTextDocument((doc) => {
			if (cfg().get<boolean>('checkOnOpen', true)) {
				void check(doc);
			}
		}),
		vscode.workspace.onDidSaveTextDocument((doc) => {
			if (cfg().get<boolean>('checkOnSave', true)) {
				void check(doc);
			}
		}),
		vscode.workspace.onDidChangeTextDocument((e) => {
			if (cfg().get<boolean>('checkOnType', false) && isMigration(e.document) && e.contentChanges.length > 0) {
				schedule(e.document);
			}
		}),
		vscode.workspace.onDidCloseTextDocument((doc) => {
			const key = doc.uri.toString();
			const st = states.get(key);
			if (st) {
				clearTimeout(st.timer);
				st.handle?.child.kill();
				states.delete(key);
			}
			diagnostics.delete(doc.uri);
		}),
		vscode.window.onDidChangeActiveTextEditor(refreshStatus),
		vscode.workspace.onDidChangeConfiguration((e) => {
			if (e.affectsConfiguration('dbguard')) {
				versionVerdict = undefined;
				warnedMissing = false;
				for (const doc of vscode.workspace.textDocuments) {
					void check(doc);
				}
			}
		}),
	);

	// Quick fix: acknowledge a risk with an auditable, in-file comment.
	context.subscriptions.push(
		vscode.languages.registerCodeActionsProvider(
			LANGUAGES.map((language) => ({ language, scheme: 'file' })),
			{
				provideCodeActions(doc, _range, ctx) {
					const style = commentStyleFor(doc.fileName);
					const actions: vscode.CodeAction[] = [];
					for (const d of ctx.diagnostics) {
						const code = typeof d.code === 'object' ? String(d.code.value) : String(d.code ?? '');
						if (d.source !== SOURCE || !code || code === 'not-analyzed' || d.severity === vscode.DiagnosticSeverity.Hint) {
							continue;
						}
						if (!style) {
							continue; // JSON has no comments: use the changeSet's "comment" field (see the README)
						}
						const a = new vscode.CodeAction(`DB Guard: acknowledge "${code}" (adds a dbguard:ignore comment)`, vscode.CodeActionKind.QuickFix);
						a.diagnostics = [d];
						a.command = { command: 'dbguard.acknowledge', title: a.title, arguments: [doc.uri, d.range.start.line, code] };
						actions.push(a);
					}
					return actions;
				},
			},
			{ providedCodeActionKinds: [vscode.CodeActionKind.QuickFix] },
		),
	);

	// Commands.
	context.subscriptions.push(
		vscode.commands.registerCommand('dbguard.checkFile', async () => {
			const doc = vscode.window.activeTextEditor?.document;
			if (!doc) {
				void vscode.window.showInformationMessage('DB Guard: open a migration file first.');
				return;
			}
			await check(doc, true);
		}),
		vscode.commands.registerCommand('dbguard.showOutput', () => output.show()),
		vscode.commands.registerCommand(
			'dbguard.acknowledge',
			async (uri: vscode.Uri, line: number, rule: string, reason?: string): Promise<boolean> => {
				const doc = await vscode.workspace.openTextDocument(uri);
				const style = commentStyleFor(doc.fileName);
				if (!style) {
					void vscode.window.showWarningMessage('DB Guard: this file type has no comments. Put the ignore in the changeSet "comment" field.');
					return false;
				}
				reason ??= await vscode.window.showInputBox({
					title: `Acknowledge "${rule}"`,
					prompt: 'Why is this risk acceptable? The reason is kept in the file, in git history and in the PR comment.',
					validateInput: (v) => (v.trim() ? undefined : 'A reason is required'),
				});
				if (reason === undefined) {
					return false;
				}
				const target = doc.lineAt(Math.min(line, doc.lineCount - 1));
				const indent = target.text.slice(0, target.firstNonWhitespaceCharacterIndex);
				const eol = doc.eol === vscode.EndOfLine.CRLF ? '\r\n' : '\n';
				const edit = new vscode.WorkspaceEdit();
				edit.insert(uri, new vscode.Position(target.lineNumber, 0), `${indent}${ignoreComment(style, rule, reason)}${eol}`);
				const ok = await vscode.workspace.applyEdit(edit);
				if (ok) {
					await check(doc);
				}
				return ok;
			},
		),
		vscode.commands.registerCommand('dbguard.showChangelog', async (table?: string, dirOverride?: string) => {
			const uri = vscode.window.activeTextEditor?.document.uri ?? vscode.workspace.workspaceFolders?.[0]?.uri;
			if (!uri) {
				void vscode.window.showInformationMessage('DB Guard: open a folder first.');
				return;
			}
			table ??= await vscode.window.showInputBox({ prompt: 'Show the schema changelog for which table? (leave empty for all tables)' });
			if (table === undefined) {
				return;
			}
			const cwd = workspaceFolderFor(uri);
			const dir = path.resolve(cwd ?? process.cwd(), dirOverride ?? cfg().get<string>('snapshotsDir', 'snapshots'));
			const args = ['--dir', dir, '--format', 'markdown'];
			if (table.trim()) {
				args.push('--table', table.trim());
			}
			try {
				const text = await runChangelog(binary(uri), args, cwd);
				const doc = await vscode.workspace.openTextDocument({ language: 'markdown', content: text });
				await vscode.window.showTextDocument(doc, { preview: true });
			} catch (e) {
				if (e instanceof BinaryNotFound) {
					reportMissing(e);
				} else {
					void vscode.window.showErrorMessage(`DB Guard: ${e instanceof Error ? e.message : String(e)}`);
				}
			}
		}),
	);

	// Check what is already open.
	for (const doc of vscode.workspace.textDocuments) {
		if (cfg().get<boolean>('checkOnOpen', true)) {
			void check(doc);
		}
	}
	refreshStatus();

	return { check };
}

export function deactivate(): void {
	// Nothing to release: subscriptions are disposed by VS Code.
}
