package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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

// locate prefixes a comment that can't be posted inline with its location.
func locate(file string, line int, body string) string {
	if file == "" {
		return body
	}
	return fmt.Sprintf("`%s:%d`\n\n%s", file, line, body)
}

// postComment posts a single comment: inline when the line is in the diff,
// otherwise (or if GitHub rejects it) as a regular PR comment.
func postComment(ctx context.Context, repo string, n int, commitID string, c Comment) (string, error) {
	if c.Inline && commitID != "" {
		payload, _ := json.Marshal(map[string]any{
			"body": c.Body, "commit_id": commitID, "path": c.File, "line": c.Line, "side": "RIGHT",
		})
		out, err := gh(ctx, payload, "api", "-X", "POST", fmt.Sprintf("repos/%s/pulls/%d/comments", repo, n), "--input", "-")
		if err == nil {
			return htmlURL(out), nil
		}
	}
	out, err := gh(ctx, []byte(locate(c.File, c.Line, c.Body)), "pr", "comment", strconv.Itoa(n), "-R", repo, "--body-file", "-")
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
			inline = append(inline, inlineComment{c.File, c.Line, "RIGHT", c.Body})
		} else {
			parts = append(parts, locate(c.File, c.Line, c.Body))
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"commit_id": commitID, "event": event, "body": strings.Join(parts, "\n\n---\n\n"), "comments": inline,
	})
	out, err := gh(ctx, payload, "api", "-X", "POST", fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, n), "--input", "-")
	if err != nil {
		return "", err
	}
	return htmlURL(out), nil
}
