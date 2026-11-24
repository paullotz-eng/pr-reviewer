# pr-reviewer

Local web app: list open PRs → click one → Claude reviews it → click a suggested
[Conventional Comment](https://conventionalcomments.org) to post it on the PR.

Requires `gh` (authenticated) and `claude` on your PATH. No Go dependencies.

```sh
go build -o pr-reviewer . && ./pr-reviewer             # PRs where your review is requested
./pr-reviewer -repo acme/shop                          # newest open PRs in one repo
./pr-reviewer -model opus -limit 50 -addr 127.0.0.1:7777 -no-open
./pr-reviewer -team acme/web-team                       # tag and auto-review a team's PRs
./pr-reviewer -h                                        # all flags
```

### Config file

Any flag can be set in `~/.config/pr-reviewer/config` (or under
`$XDG_CONFIG_HOME`), one `name = value` per line. Flags on the command line win.
Keep team-specific settings here rather than in the code:

```ini
# ~/.config/pr-reviewer/config
team = acme/web-team
repo = acme/shop
conventions = docs/frontend-conventions.md
conventions-file = /path/to/local/frontend-conventions.md
```

- **Search:** typing filters the list (title, author, repo, number). Enter runs a
  GitHub search within `-repo`, or the team's org. Qualifiers like `author:x` work.
- **Team tags:** PRs by members of `-team` get a `team` tag, yours get `you`.
  "Only my team" hides the rest.
- **Frontend conventions:** with `-conventions <path>`, if a PR touches
  `.tsx`/`.jsx`/`use-*.ts`, that file from the PR's base branch is added to the
  review (falls back to `-conventions-file`). Only important violations are
  flagged, tagged with the `conventions` topic.

- **Linked issues:** issues the PR closes (`Closes #123`) are loaded so the
  reviewer can check the PR against their acceptance criteria.
- **Watch mode:** every 10 minutes (`-watch 2m` to change, `-watch 0` to turn
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

- **Comment format:** matches the [Pullpo Conventional Comments extension](https://github.com/pullpo-io/conventional-comments)
  the team uses, so posted comments look the same as hand-written ones and the
  extension recognises them: its badge style by default, or
  `issue(blocking): subject` with `-cc-style plain`. One decoration per comment
  (`blocking`, `non-blocking`, `if-minor`); topics such as `security` or
  `pre-existing` are shown in the app but not posted.

- Comments are sorted by severity (critical → info), then importance (10 → 1).
- Comments with a file/line are posted as inline review comments on the PR head
  commit. If GitHub rejects the line, it falls back to a normal PR comment
  prefixed with `file:line`.
- Reviews and posted state are cached in memory until restart. "Re-run review"
  forces a fresh review.
- The reviewer instructions live in `prompt.md` (embedded at build time). Edit and rebuild
  to tune the review style.
