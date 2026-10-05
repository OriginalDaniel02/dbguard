package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/OriginalDaniel02/dbguard/internal/gitlab"
	"github.com/OriginalDaniel02/dbguard/internal/report"
)

// noMigrationsBody replaces an earlier warning once the MR no longer touches migrations.
const noMigrationsBody = report.Marker + "\n\n## DB Guard: no migration files changed in this merge request\n\nEarlier findings no longer apply.\n"

// gitlabCommentCmd posts (or updates) the single DB Guard comment on a GitLab merge
// request. It reads GitLab's predefined CI variables, so in a pipeline it needs only
// a token. The token is read from an environment variable, never from a flag.
func gitlabCommentCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gitlab-comment", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `usage: dbguard check --format markdown ... | dbguard gitlab-comment [flags]

Posts stdin as the DB Guard comment on the current GitLab merge request, editing the
existing comment if there is one. Uses CI_API_V4_URL, CI_PROJECT_ID and
CI_MERGE_REQUEST_IID. The token needs the "api" scope (CI_JOB_TOKEN cannot comment).

flags:`)
		fs.PrintDefaults()
	}
	tokenEnv := fs.String("token-env", "DBGUARD_GITLAB_TOKEN", "name of the env var holding a GitLab access token with the api scope")
	noMigrations := fs.Bool("no-migrations", false, "the MR has no migration changes: update an existing comment (never create one)")
	onlyUpdate := fs.Bool("only-update", false, "update an existing comment but never create one")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	need := map[string]string{
		"CI_API_V4_URL":        os.Getenv("CI_API_V4_URL"),
		"CI_PROJECT_ID":        os.Getenv("CI_PROJECT_ID"),
		"CI_MERGE_REQUEST_IID": os.Getenv("CI_MERGE_REQUEST_IID"),
		"$" + *tokenEnv:        os.Getenv(*tokenEnv),
	}
	for k, v := range need {
		if v == "" {
			fmt.Fprintf(stderr, "dbguard: %s is not set (is this a merge request pipeline?)\n", k)
			return 2
		}
	}

	body := noMigrationsBody
	if !*noMigrations {
		b, err := io.ReadAll(stdin)
		if err != nil || len(b) == 0 {
			fmt.Fprintln(stderr, "dbguard: no comment body on stdin")
			return 2
		}
		body = string(b)
	}
	c := &gitlab.Client{
		BaseURL: need["CI_API_V4_URL"], Token: need["$"+*tokenEnv],
		ProjectID: need["CI_PROJECT_ID"], MRIID: need["CI_MERGE_REQUEST_IID"],
	}
	action, err := c.Upsert(context.Background(), report.Marker, body, *noMigrations || *onlyUpdate)
	if err != nil {
		fmt.Fprintln(stderr, "dbguard:", err)
		return 2
	}
	fmt.Fprintf(stdout, "dbguard: GitLab comment %s\n", action)
	return 0
}
