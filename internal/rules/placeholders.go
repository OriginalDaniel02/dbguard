package rules

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholderRe = regexp.MustCompile(`\$\{([^}\n]+)\}`)

// substitutePlaceholders replaces Flyway ${name} placeholders so the SQL parses.
// Names with a supplied value are replaced by it; others become a unique
// identifier-shaped stand-in. The returned func maps stand-ins back to ${name}.
func substitutePlaceholders(sql string, vals map[string]string) (string, func(string) string) {
	back := map[string]string{}
	ids := map[string]string{}
	out := placeholderRe.ReplaceAllStringFunc(sql, func(m string) string {
		name := placeholderRe.FindStringSubmatch(m)[1]
		if v, ok := vals[name]; ok {
			return v
		}
		id, ok := ids[name]
		if !ok {
			id = fmt.Sprintf("dbguard_ph_%d_x", len(ids)+1)
			ids[name] = id
			back[id] = m
		}
		return id
	})
	return out, func(s string) string {
		for id, orig := range back {
			s = strings.ReplaceAll(s, id, orig)
		}
		return s
	}
}
