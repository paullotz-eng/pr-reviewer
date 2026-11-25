package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

type PR struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
	IsDraft   bool      `json:"isDraft"`
	Team      bool      `json:"team"` // author is in your team
	Mine      bool      `json:"mine"` // author is you
	// Review status for the list: Status is the text, StatusKind picks the colour
	// (running, blocking, comments, clean, posted). Empty when not reviewed.
	Status     string `json:"status,omitempty"`
	StatusKind string `json:"statusKind,omitempty"`
}

type PRDetails struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	Files       []struct {
		Path string `json:"path"`
	} `json:"files"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	ClosingIssuesReferences []struct {
		URL string `json:"url"`
	} `json:"closingIssuesReferences"`
}

func gh(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %v: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// listPRs returns open PRs, newest first. Without a query: the repo's PRs, or PRs
// where your review is requested. With a query: a GitHub search within the repo or org.
func listPRs(ctx context.Context, repo, org, query string, limit int) ([]PR, error) {
	args := []string{"search", "prs", "--state=open", "--sort=created", "--order=desc",
		"--limit", strconv.Itoa(limit), "--json", "number,title,author,createdAt,url,isDraft,repository"}
	switch {
	case repo != "":
		args = append(args, "--repo", repo)
	case query != "" && org != "":
		args = append(args, "--owner", org)
	case query == "":
		args = append(args, "--review-requested=@me")
	}
	if query != "" {
		args = append(args, "--", query)
	}
	out, err := gh(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number     int       `json:"number"`
		Title      string    `json:"title"`
		CreatedAt  time.Time `json:"createdAt"`
		URL        string    `json:"url"`
		IsDraft    bool      `json:"isDraft"`
		Author     struct{ Login string }
		Repository struct{ NameWithOwner string }
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	prs := make([]PR, len(raw))
	for i, r := range raw {
		prs[i] = PR{Repo: r.Repository.NameWithOwner, Number: r.Number, Title: r.Title, Author: r.Author.Login, CreatedAt: r.CreatedAt, URL: r.URL, IsDraft: r.IsDraft}
	}
	return prs, nil
}

// teamPRs returns open, non-draft PRs by members, within repo or else org.
// It uses the search API directly: there, repeated author: qualifiers are ORed,
// which `gh search prs` does not support. Queries are capped at 256 characters,
// so large teams are split over several searches.
func teamPRs(ctx context.Context, repo, org string, members map[string]bool) ([]PR, error) {
	base := "is:pr is:open draft:false org:" + org
	if repo != "" {
		base = "is:pr is:open draft:false repo:" + repo
	}
	logins := slices.Sorted(maps.Keys(members))
	var prs []PR
	for len(logins) > 0 {
		q := base
		for len(logins) > 0 && len(q)+len(" author:")+len(logins[0]) <= 256 {
			q += " author:" + logins[0]
			logins = logins[1:]
		}
		if q == base { // a single login that doesn't fit; skip it
			logins = logins[1:]
			continue
		}
		out, err := gh(ctx, nil, "api", "-X", "GET", "search/issues", "-f", "q="+q, "-f", "sort=created", "-f", "order=desc", "-f", "per_page=100")
		if err != nil {
			return nil, err
		}
		var res struct {
			Items []struct {
				Number        int       `json:"number"`
				Title         string    `json:"title"`
				CreatedAt     time.Time `json:"created_at"`
				HTMLURL       string    `json:"html_url"`
				Draft         bool      `json:"draft"`
				RepositoryURL string    `json:"repository_url"`
				User          struct{ Login string }
			}
		}
		if err := json.Unmarshal(out, &res); err != nil {
			return nil, err
		}
		for _, it := range res.Items {
			_, name, _ := strings.Cut(it.RepositoryURL, "/repos/")
			prs = append(prs, PR{Repo: name, Number: it.Number, Title: it.Title, Author: it.User.Login, CreatedAt: it.CreatedAt, URL: it.HTMLURL, IsDraft: it.Draft})
		}
	}
	return prs, nil
}

// mergePRs combines PR lists without duplicates, newest first.
func mergePRs(lists ...[]PR) []PR {
	seen := map[string]bool{}
	var all []PR
	for _, l := range lists {
		for _, pr := range l {
			if k := prKey(pr.Repo, pr.Number); !seen[k] {
				seen[k] = true
				all = append(all, pr)
			}
		}
	}
	slices.SortStableFunc(all, func(a, b PR) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return all
}

// teamMembers returns the logins of an "org/team-slug" GitHub team.
func teamMembers(ctx context.Context, team string) (map[string]bool, error) {
	org, slug, ok := strings.Cut(team, "/")
	if !ok {
		return nil, fmt.Errorf("team must be org/slug, got %q", team)
	}
	out, err := gh(ctx, nil, "api", "--paginate", fmt.Sprintf("orgs/%s/teams/%s/members", org, slug), "-q", ".[].login")
	if err != nil {
		return nil, err
	}
	members := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		members[l] = true
	}
	return members, nil
}

func currentUser(ctx context.Context) (string, error) {
	out, err := gh(ctx, nil, "api", "user", "-q", ".login")
	return strings.TrimSpace(string(out)), err
}

// repoFile returns a file's raw content from a repo at a ref.
func repoFile(ctx context.Context, repo, ref, path string) (string, error) {
	out, err := gh(ctx, nil, "api", "-H", "Accept: application/vnd.github.raw",
		fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, path, ref))
	return string(out), err
}

func prDetails(ctx context.Context, repo string, n int) (*PRDetails, error) {
	out, err := gh(ctx, nil, "pr", "view", strconv.Itoa(n), "-R", repo,
		"--json", "title,body,author,baseRefName,headRefName,headRefOid,files,closingIssuesReferences")
	if err != nil {
		return nil, err
	}
	var d PRDetails
	return &d, json.Unmarshal(out, &d)
}

// linkedIssues returns the title and body of the issues the PR closes, so the
// reviewer can check the PR against their acceptance criteria.
func linkedIssues(ctx context.Context, d *PRDetails) string {
	var b strings.Builder
	for i, ref := range d.ClosingIssuesReferences {
		if i == 3 {
			break
		}
		out, err := gh(ctx, nil, "issue", "view", ref.URL, "--json", "title,body")
		if err != nil {
			fmt.Fprintf(&b, "- %s (could not load)\n", ref.URL)
			continue
		}
		var is struct{ Title, Body string }
		json.Unmarshal(out, &is)
		fmt.Fprintf(&b, "### %s\n%s\n\n%s\n\n", is.Title, ref.URL, truncate(strings.TrimSpace(is.Body), 4000))
	}
	return b.String()
}

func prDiff(ctx context.Context, repo string, n int) (string, error) {
	out, err := gh(ctx, nil, "pr", "diff", strconv.Itoa(n), "-R", repo)
	return string(out), err
}

func prHeadSHA(ctx context.Context, repo string, n int) (string, error) {
	out, err := gh(ctx, nil, "pr", "view", strconv.Itoa(n), "-R", repo, "--json", "headRefOid", "-q", ".headRefOid")
	return strings.TrimSpace(string(out)), err
}

// prDiscussion returns the existing comments and reviews on a PR as a compact list,
// so the reviewer doesn't repeat what others already said.
func prDiscussion(ctx context.Context, repo string, n int) (string, error) {
	var b strings.Builder
	add := func(who, where, body string) {
		body = strings.TrimSpace(body)
		if body == "" || b.Len() > 15_000 {
			return
		}
		fmt.Fprintf(&b, "- @%s%s: %s\n", who, where, truncate(strings.ReplaceAll(body, "\n", " "), 400))
	}

	out, err := gh(ctx, nil, "pr", "view", strconv.Itoa(n), "-R", repo, "--json", "comments,reviews")
	if err != nil {
		return "", err
	}
	var conv struct {
		Comments []struct {
			Author struct{ Login string }
			Body   string
		}
		Reviews []struct {
			Author struct{ Login string }
			State  string
			Body   string
		}
	}
	if err := json.Unmarshal(out, &conv); err != nil {
		return "", err
	}
	for _, r := range conv.Reviews {
		add(r.Author.Login, " ("+strings.ToLower(r.State)+")", r.Body)
	}
	for _, c := range conv.Comments {
		add(c.Author.Login, "", c.Body)
	}

	out, err = gh(ctx, nil, "api", "--paginate", fmt.Sprintf("repos/%s/pulls/%d/comments", repo, n))
	if err != nil {
		return "", err
	}
	var inline []struct {
		User         struct{ Login string }
		Path         string
		OriginalLine int `json:"original_line"`
		Body         string
	}
	if err := json.Unmarshal(out, &inline); err != nil {
		return "", err
	}
	for _, c := range inline {
		add(c.User.Login, fmt.Sprintf(" on %s:%d", c.Path, c.OriginalLine), c.Body)
	}
	return b.String(), nil
}

func htmlURL(out []byte) string {
	var res struct {
		HTMLURL string `json:"html_url"`
	}
	json.Unmarshal(out, &res)
	return res.HTMLURL
}

// locate appends the location to a comment that can't be posted inline. It goes
// at the end so the comment still starts with its Conventional Comments prefix.
func locate(file string, line int, body string) string {
	if file == "" {
		return body
	}
	return fmt.Sprintf("%s\n\n`%s:%d`", body, file, line)
}

// signature closes every comment posted to GitHub, so readers can tell it came
// from this tool. Added at posting time, so it never shows up in the editor.
const signature = `<sub><a href="https://github.com/paullotz-eng/pr-reviewer"><img src="https://raw.githubusercontent.com/paullotz-eng/pr-reviewer/main/avatar.svg" width="16" height="16" align="absmiddle" alt="pr-reviewer"></a></sub>`

func sign(body string) string { return body + "\n\n" + signature }

// postComment posts a single comment: inline when the line is in the diff,
// otherwise (or if GitHub rejects it) as a regular PR comment.
func postComment(ctx context.Context, repo string, n int, commitID string, c Comment) (string, error) {
	if c.Inline && commitID != "" {
		payload, _ := json.Marshal(map[string]any{
			"body": sign(c.Body), "commit_id": commitID, "path": c.File, "line": c.Line, "side": "RIGHT",
		})
		out, err := gh(ctx, payload, "api", "-X", "POST", fmt.Sprintf("repos/%s/pulls/%d/comments", repo, n), "--input", "-")
		if err == nil {
			return htmlURL(out), nil
		}
	}
	out, err := gh(ctx, []byte(sign(locate(c.File, c.Line, c.Body))), "pr", "comment", strconv.Itoa(n), "-R", repo, "--body-file", "-")
	return strings.TrimSpace(string(out)), err
}

// submitReview posts one GitHub review with all comments: inline ones on their
// lines, the rest appended to the review body. event is APPROVE, COMMENT or REQUEST_CHANGES.
func submitReview(ctx context.Context, repo string, n int, commitID, event, body string, comments []Comment) (string, error) {
	type inlineComment struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
		Body string `json:"body"`
	}
	inline := []inlineComment{}
	parts := []string{}
	if strings.TrimSpace(body) != "" {
		parts = append(parts, strings.TrimSpace(body))
	}
	for _, c := range comments {
		if c.Inline {
			inline = append(inline, inlineComment{c.File, c.Line, "RIGHT", sign(c.Body)})
		} else {
			parts = append(parts, locate(c.File, c.Line, c.Body))
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"commit_id": commitID, "event": event, "body": sign(strings.Join(parts, "\n\n---\n\n")), "comments": inline,
	})
	out, err := gh(ctx, payload, "api", "-X", "POST", fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, n), "--input", "-")
	if err != nil {
		return "", err
	}
	return htmlURL(out), nil
}
