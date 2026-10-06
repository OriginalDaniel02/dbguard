package javamig

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tb builds a text block expression from its lines (joined with LF).
func tb(lines ...string) string { return strings.Join(lines, "\n") }

// TestLexerMatchesRealJava compiles real Java with the JDK and requires the lexer to
// evaluate every string constant to exactly the same bytes. It is skipped when no JDK is installed.
func TestLexerMatchesRealJava(t *testing.T) {
	javac, err := exec.LookPath("javac")
	if err != nil {
		t.Skip("javac not found: skipping the differential test against real Java")
	}
	java, err := exec.LookPath("java")
	if err != nil {
		t.Skip("java not found")
	}

	cases := []struct{ name, expr string }{
		{"plain", `"plain"`},
		{"escapes", `"tab\tnl\nq\"b\\u\u0041\101\60 \377 \12"`},
		{"escape s", `"a\sb\s"`},
		{"simple block", tb(`"""`, `    SELECT 1`, `      indented`, `    """`)},
		{"closing on content line", tb(`"""`, `    a`, `    b"""`)},
		{"closing delimiter less indented", tb(`"""`, `        a`, `          b`, `      """`)},
		{"blank line inside", tb(`"""`, `    a`, ``, `    b`, `    """`)},
		{"trailing spaces stripped, escapes kept", tb(`"""`, `    a   `, `    b\s`, `    c  \040`, `    """`)},
		{"line continuation", tb(`"""`, `    a \`, `    b`, `    """`)},
		{"escaped triple quote", tb(`"""`, `    say \"""hi\"""`, `    """`)},
		{"tab indentation", tb(`"""`, "\t\ta", "\t\t\tb", "\t\t\"\"\"")},
		{"concatenation", `"a" + "b" + 1 + 2 + "c"`},
		{"empty block", tb(`"""`, `    """`)},
		{"unicode and newline escapes", tb(`"""`, `    caf\u00e9 \n x`, `    """`)},
		{"CRLF source", "\"\"\"\r\n    a\r\n    b\r\n    \"\"\""},
		{"closing delimiter more indented", tb(`"""`, `  a`, `  b`, `        """`)},
		{"whitespace-only line is emptied", tb(`"""`, `    a`, `            `, `    b`, `    """`)},
		{"non-ASCII in source", `"naïve ✓"`},
		{"tab escape in block", tb(`"""`, `    a\tb`, `    """`)},
		{"quotes inside block", tb(`"""`, `    "quoted" and ""two""`, `    """`)},
		{"sql shaped block", tb(`"""`, `        ALTER TABLE t`, `            ADD COLUMN a int;`, ``, `        CREATE INDEX i ON t (a);`, `        """`)},
		{"concatenated blocks and strings", tb(`"""`, `    a`, `    """ + "-" + """`, `    b`, `    """`)},
	}

	var src strings.Builder
	src.WriteString("import java.nio.charset.StandardCharsets;\nimport java.util.HexFormat;\npublic class D {\n")
	for i, c := range cases {
		fmt.Fprintf(&src, "  static final String S%d = %s;\n", i, c.expr)
	}
	src.WriteString("  public static void main(String[] args) {\n")
	for i := range cases {
		fmt.Fprintf(&src, "    System.out.println(HexFormat.of().formatHex(S%d.getBytes(StandardCharsets.UTF_8)));\n", i)
	}
	src.WriteString("  }\n}\n")

	dir := t.TempDir()
	file := filepath.Join(dir, "D.java")
	if err := os.WriteFile(file, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(javac, "-encoding", "UTF-8", "-d", dir, file).CombinedOutput(); err != nil {
		t.Fatalf("javac rejected the generated source (a test bug):\n%s\n%s", out, src.String())
	}
	out, err := exec.Command(java, "-cp", dir, "D").Output()
	if err != nil {
		t.Fatal(err)
	}
	// One line per case. An empty string prints an empty line, so split on newlines rather than fields.
	want := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	want = want[:len(want)-1] // the newline after the last line
	if len(want) != len(cases) {
		t.Fatalf("java printed %d values for %d cases", len(want), len(cases))
	}

	got := values(t, src.String())
	if len(got) < len(cases) {
		t.Fatalf("lexer found %d groups, want at least %d", len(got), len(cases))
	}
	for i, c := range cases {
		if h := hex.EncodeToString([]byte(got[i])); h != want[i] {
			wb, _ := hex.DecodeString(want[i])
			t.Errorf("%s:\n  java:  %q\n  dbguard: %q", c.name, string(wb), got[i])
		}
	}
}
