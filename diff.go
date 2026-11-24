package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// generatedRe matches lockfiles and generated files that only waste review tokens.
var generatedRe = regexp.MustCompile(`(^|/)(yarn\.lock|package-lock\.json|pnpm-lock\.yaml|go\.sum|Cargo\.lock|composer\.lock|Gemfile\.lock|poetry\.lock)$` +
	`|\.snap$|\.min\.(js|css)$|\.generated\.|(^|/)(__generated__|generated|dist|vendor)/`)

var diffHeaderRe = regexp.MustCompile(`^diff --git a/.* b/(.*)$`)

// filterDiff drops lockfiles and generated files from a unified diff.
func filterDiff(diff string) (kept string, skipped []string) {
	var b strings.Builder
	skip := false
	for _, l := range strings.SplitAfter(diff, "\n") {
		if m := diffHeaderRe.FindStringSubmatch(strings.TrimRight(l, "\n")); m != nil {
			skip = generatedRe.MatchString(m[1])
			if skip {
				skipped = append(skipped, m[1])
			}
		}
		if !skip {
			b.WriteString(l)
		}
	}
	return b.String(), skipped
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)

// annotateDiff prefixes added and context lines with their new-file line number
// so the model can reference exact lines. It also returns which (file, line)
// pairs are in the diff, because GitHub only accepts inline comments on those.
func annotateDiff(diff string) (string, map[string]map[int]bool) {
	var b strings.Builder
	valid := map[string]map[int]bool{}
	file, line, inHunk := "", 0, false
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git"):
			inHunk, file = false, ""
			b.WriteString(l)
		case !inHunk && strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if valid[file] == nil {
				valid[file] = map[int]bool{}
			}
			b.WriteString(l)
		case strings.HasPrefix(l, "@@"):
			if m := hunkRe.FindStringSubmatch(l); m != nil {
				line, _ = strconv.Atoi(m[1])
			}
			inHunk = true
			b.WriteString(l)
		case inHunk && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ")):
			fmt.Fprintf(&b, "%5d %s", line, l)
			if file != "" {
				valid[file][line] = true
			}
			line++
		case inHunk && strings.HasPrefix(l, "-"):
			fmt.Fprintf(&b, "      %s", l)
		default:
			b.WriteString(l)
		}
		b.WriteByte('\n')
	}
	return b.String(), valid
}
