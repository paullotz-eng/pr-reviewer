# pr-reviewer

Local web app: list open PRs → click one → Claude reviews it → click a suggested
[Conventional Comment](https://conventionalcomments.org) to post it on the PR.

Requires `gh` (authenticated) and `claude` on your PATH. No Go dependencies.

```sh
go build -o pr-reviewer . && ./pr-reviewer             # PRs where your review is requested
./pr-reviewer -repo eversport/eversports               # newest open PRs in one repo
./pr-reviewer -model opus -limit 50 -addr 127.0.0.1:7777 -no-open
./pr-reviewer -team eversport/guild-react                # tag a different team's PRs
```

- **Search:** typing filters the list (title, author, repo, number). Enter runs a
  GitHub search within `-repo`, or the team's org. Qualifiers like `author:x` work.
- **Team tags:** PRs by members of `-team` get a `team` tag, yours get `you`.
  "Only my team" hides the rest.
- **React conventions:** if a PR touches `.tsx`/`.jsx`/`use-*.ts`, the
  `.claude/skills/react-conventions/SKILL.md` from the PR's base branch is added
  to the review (falls back to `-skill-file`). Only important violations are
  flagged, tagged with the `conventions` decoration.

- **Linked issues:** issues the PR closes (`Closes #123`) are loaded so the
  reviewer can check the PR against their acceptance criteria.
- **Watch mode:** every 5 minutes (`-watch 2m` to change, `-watch 0` to turn
  off) the app checks for open, non-draft PRs by `-team` members (not you) in
  `-repo`, or else the team's org. New ones are reviewed in the background, one
  at a time, and you get a desktop notification with the verdict. Nothing is
  posted. On startup it also catches PRs opened in the last 24h that have no
  review yet. The PR list refreshes every minute and shows `reviewing…` or the
  stored verdict.
- **Challenger:** blocking, critical and high findings get a second, fresh
  Claude pass that tries to refute them. Refuted findings are hidden (expand
  "refuted by the challenger" to see them), unresolved ones become non-blocking,
  and the verdict drops from request changes to comment if nothing blocks anymore.

- Comments are sorted by severity (critical → info), then importance (10 → 1).
- Comments with a file/line are posted as inline review comments on the PR head
  commit. If GitHub rejects the line, it falls back to a normal PR comment
  prefixed with `file:line`.
- Reviews and posted state are cached in memory until restart. "Re-run review"
  forces a fresh review.
- The reviewer instructions live in `prompt.md` (embedded at build time). Edit and rebuild
  to tune the review style.
