// Package flyway locates Flyway SQL migrations using Flyway's own naming
// convention (V<version>__<desc>.sql, R__<desc>.sql); no separate config.
package flyway

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
)

var name = regexp.MustCompile(`^(V[0-9][0-9._]*|R)__.+\.sql$`)

// IsMigration reports whether a file name follows Flyway's SQL naming convention.
func IsMigration(base string) bool { return name.MatchString(base) }

var javaName = regexp.MustCompile(`^(V[0-9][0-9._]*|R)__.+\.java$`)

// IsJavaMigration reports whether a file name follows Flyway's naming convention for
// Java migrations (V2__Add_index.java: the class name is the file name).
func IsJavaMigration(base string) bool { return javaName.MatchString(base) }

// Collect expands paths into migration files. Explicit files are taken as-is
// (so CI can pass exactly the files a PR changed); directories are walked for
// Flyway-named files.
func Collect(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if path == p || IsMigration(d.Name()) { // p itself: explicit file
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}
