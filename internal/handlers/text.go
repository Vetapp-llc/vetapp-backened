package handlers

import (
	"regexp"
	"strings"
)

// brTagPattern matches the various <br> shapes the legacy PHP app
// emitted via `nl2br()`, plus an optional trailing `\r?\n`. PHP
// inserts `<br />` *before* every existing newline, so consuming that
// trailing newline gives the cleanest result:
//
//	"line<br />\r\nnext" → "line\nnext"
//	"line<br />next"     → "line\nnext"   (no surrounding newline)
//
// Without consuming the newline we'd end up with awkward `\n\r\n`
// sequences in stripped text. Case-insensitive to also catch `<BR>`.
// Single compiled regex avoids repeated allocations on every read.
var brTagPattern = regexp.MustCompile(`(?i)<br\s*/?>(\r?\n)?`)

// cleanText converts legacy HTML-flavored multi-line text into plain
// text suitable for a `<Text>` component in React Native or for plain
// rendering in Next.js without `dangerouslySetInnerHTML`.
//
// Specifically:
//   - Replaces all `<br>` / `<br/>` / `<br />` (any whitespace and case)
//     with literal newlines so the UI renders line breaks naturally.
//   - Trims trailing whitespace from each line and any leading/trailing
//     whitespace from the whole string. PHP forms add quirky whitespace
//     because users hit Enter at the end of paragraphs.
//   - Collapses runs of 3+ blank lines down to two (one blank line),
//     matching what a sane editor would produce.
//
// Empty input returns empty. The function is idempotent — running it
// twice returns the same result.
//
// Used on every textual field the API returns to clients; new writes
// from mobile/Next.js save plain text only, so this no-ops on them.
func cleanText(s string) string {
	if s == "" {
		return ""
	}

	// 1. <br> → \n
	out := brTagPattern.ReplaceAllString(s, "\n")

	// 2. Trim trailing whitespace from each line. We keep leading
	//    whitespace because some users intentionally indent.
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	out = strings.Join(lines, "\n")

	// 3. Collapse 3+ consecutive newlines into 2. The regex
	//    `\n{3,}` would also work but a manual loop avoids another
	//    package import and is plenty fast for short text.
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}

	// 4. Trim outer whitespace.
	return strings.TrimSpace(out)
}
