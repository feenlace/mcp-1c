package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The guards in this file are STATIC. They read the workflow file and check the
// shape of the commands it would run; nothing here dispatches a workflow or
// talks to GitHub, so they cannot prove the job passes in CI. What they can
// prove is the failure the job had: a gh call that has no way to learn which
// repository it operates on.
//
// gh resolves the repository from, in order, the -R/--repo flag, the GH_REPO
// environment variable, or the git remotes of the working directory. The last
// one is unavailable to a job that never checks the repository out: gh then
// exits with "failed to run git: fatal: not a git repository". So in a job
// without a checkout every gh call must be covered by one of the first two.

// verifyWorkflowPath returns the path of the release-asset verification
// workflow relative to this test's directory (cmd/mcp-1c), the same way
// version_test.go reaches the bundled extension.
func verifyWorkflowPath() string {
	return filepath.Join("..", "..", ".github", "workflows", "verify-release-assets.yml")
}

func readWorkflow(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workflow %s: %v", path, err)
	}
	return string(raw)
}

// ghCallRe matches an invocation of the gh CLI: the word gh at the start of a
// command position (line start, or after a pipe, semicolon, &&, subshell) and
// followed by a subcommand. Anchored this way so that a word such as "gh" in
// prose, or a path ending in gh, is not mistaken for a call.
var ghCallRe = regexp.MustCompile(`(?:^|[|;&(]|\s)gh\s+([a-z][a-z-]*)`)

// ghAPIWithExplicitRepoRe matches a gh api endpoint that names the repository
// inside the URL path (repos/OWNER/REPO/...). Such a call needs no repository
// resolution at all; it is the one shape that is safe without GH_REPO or -R.
var ghAPIWithExplicitRepoRe = regexp.MustCompile(`repos/[^"'\s]+/[^"'\s]+/`)

// ghCallLines returns every line of the workflow that invokes gh, skipping
// comment lines so that the explanatory prose above a step cannot be counted as
// a call.
func ghCallLines(workflow string) []string {
	var out []string
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if ghCallRe.MatchString(line) {
			out = append(out, trimmed)
		}
	}
	return out
}

// jobLevelEnv returns the key/value pairs of every job-level env block in the
// workflow. Job level is indentation 4: jobs: is at 0, the job id at 2, its
// keys (including env:) at 4, and the entries at 6. A step-level env sits at 8
// and is deliberately NOT collected, because it would cover one step only.
func jobLevelEnv(workflow string) map[string]string {
	env := map[string]string{}
	lines := strings.Split(workflow, "\n")
	for i, line := range lines {
		if line != "    env:" {
			continue
		}
		for _, entry := range lines[i+1:] {
			if strings.TrimSpace(entry) == "" {
				continue
			}
			indent := len(entry) - len(strings.TrimLeft(entry, " "))
			if indent <= 4 {
				break
			}
			key, value, ok := strings.Cut(strings.TrimSpace(entry), ":")
			if !ok {
				continue
			}
			env[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return env
}

// TestVerifyReleaseAssetsWorkflow_EveryGhCallResolvesTheRepo is the guard for
// the defect: "gh release download" was called with no -R and no GH_REPO in a
// job that never checks out the repository, so gh had nothing to resolve the
// repository from and the download, and with it the whole sha256sum -c
// verification RELEASING.md promises, could never run.
func TestVerifyReleaseAssetsWorkflow_EveryGhCallResolvesTheRepo(t *testing.T) {
	workflow := readWorkflow(t, verifyWorkflowPath())

	calls := ghCallLines(workflow)
	if len(calls) == 0 {
		t.Fatal("no gh invocation found in the workflow; this guard would pass vacuously, " +
			"so either the matcher is broken or the job no longer uses gh")
	}

	env := jobLevelEnv(workflow)
	repo, jobWide := env["GH_REPO"]

	for _, call := range calls {
		switch {
		case jobWide:
			// One job-level variable covers this call and every future one.
		case strings.Contains(call, "-R ") || strings.Contains(call, "--repo "):
			// The call names the repository itself.
		case strings.Contains(call, "gh api") && ghAPIWithExplicitRepoRe.MatchString(call):
			// The endpoint carries repos/OWNER/REPO in the URL.
		default:
			t.Errorf("gh call cannot resolve the repository (no job-level GH_REPO, no -R, "+
				"no repos/OWNER/REPO in the endpoint), and this job has no checkout to "+
				"infer one from:\n\t%s", call)
		}
	}

	if !jobWide {
		t.Error("the job defines no job-level GH_REPO, so every future gh call added to it " +
			"has to remember -R; one job-level GH_REPO covers them all")
		return
	}
	if repo != "${{ github.repository }}" {
		t.Errorf("job-level GH_REPO = %q, want ${{ github.repository }} so the job verifies "+
			"the repository it runs in and not a hardcoded one", repo)
	}
}

// usesCheckout reports whether the workflow has a step that runs the checkout
// action. It looks for a `uses:` VALUE and ignores comment lines: the workflow
// explains in a comment why it has no checkout, and a plain substring search
// would be falsified by the very sentence documenting the fact.
func usesCheckout(workflow string) bool {
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		_, value, ok := strings.Cut(trimmed, "uses:")
		if ok && strings.Contains(value, "actions/checkout") {
			return true
		}
	}
	return false
}

// TestVerifyReleaseAssetsWorkflow_HasNoCheckout pins the premise of the guard
// above. The job downloads published release assets and needs no source, so it
// runs no checkout step, which is exactly why gh cannot fall back to the git
// remotes. If a checkout is ever added on purpose, this test is the place that
// says the reasoning above has to be revisited; the GH_REPO requirement itself
// stays correct either way.
func TestVerifyReleaseAssetsWorkflow_HasNoCheckout(t *testing.T) {
	// Positive control first: a scan that cannot see a checkout step would pass
	// this test for the wrong reason, and would keep passing after one is added.
	if !usesCheckout("jobs:\n  verify:\n    steps:\n      - uses: actions/checkout@v4\n") {
		t.Fatal("the checkout scan does not detect a checkout step handed to it directly")
	}
	if usesCheckout(readWorkflow(t, verifyWorkflowPath())) {
		t.Fatal("the job now checks the repository out; re-read the reasoning in " +
			"release_workflow_test.go before relaxing anything, and keep GH_REPO")
	}
	// The other workflow DOES check out, which is the negative control for the
	// scan: it proves the two files are distinguished by what they contain and
	// not by the scan quietly returning false everywhere.
	if !usesCheckout(readWorkflow(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))) {
		t.Fatal("release.yml no longer checks out; the checkout scan lost its real-file control")
	}
}

// TestReleaseWorkflows_ParseAsSequencesOfSteps is a cheap structural sanity
// check over BOTH workflow files: a file that no longer has the keys a workflow
// needs would silently stop running. It is not a YAML validator; the YAML
// itself is parsed outside the Go test suite.
func TestReleaseWorkflows_ParseAsSequencesOfSteps(t *testing.T) {
	for _, name := range []string{"verify-release-assets.yml", "release.yml"} {
		path := filepath.Join("..", "..", ".github", "workflows", name)
		workflow := readWorkflow(t, path)
		for _, key := range []string{"name:", "on:", "jobs:", "runs-on:", "steps:"} {
			if !strings.Contains(workflow, key) {
				t.Errorf("%s has no %q key", name, key)
			}
		}
	}
}
