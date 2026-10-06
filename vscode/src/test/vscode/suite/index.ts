import * as fs from 'fs';
import * as path from 'path';
import Mocha from 'mocha';

// A VS Code test window does not reliably forward its console output on every platform, so
// the results are also written to the file named by DBGUARD_TEST_RESULTS (read by runTest.ts).
const resultsFile = process.env.DBGUARD_TEST_RESULTS;

function log(line: string): void {
	console.log(line);
	if (resultsFile) {
		try {
			fs.appendFileSync(resultsFile, line + '\n');
		} catch {
			// best effort
		}
	}
}

export async function run(): Promise<void> {
	try {
		const mocha = new Mocha({ ui: 'tdd', timeout: 120_000, reporter: 'spec' });
		for (const f of fs.readdirSync(__dirname)) {
			if (f.endsWith('.test.js')) {
				mocha.addFile(path.join(__dirname, f));
			}
		}
		await new Promise<void>((resolve, reject) => {
			const runner = mocha.run((failures) => (failures > 0 ? reject(new Error(`${failures} test(s) failed`)) : resolve()));
			runner.on('pass', (t) => log(`  PASS  ${t.fullTitle()} (${t.duration ?? 0}ms)`));
			runner.on('fail', (t, err) => log(`  FAIL  ${t.fullTitle()}\n        ${String(err && err.stack ? err.stack : err).split('\n').slice(0, 6).join('\n        ')}`));
			runner.on('pending', (t) => log(`  SKIP  ${t.fullTitle()}`));
		});
		log('ALL TESTS PASSED');
	} catch (e) {
		log(`SUITE ERROR: ${e instanceof Error ? e.stack ?? e.message : String(e)}`);
		throw e;
	}
}
