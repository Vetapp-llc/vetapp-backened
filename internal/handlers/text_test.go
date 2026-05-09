package handlers

import "testing"

func TestCleanText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty input returns empty",
			in:   "",
			want: "",
		},
		{
			name: "plain text untouched",
			in:   "Hello world",
			want: "Hello world",
		},
		{
			name: "single self-closing br tag becomes newline",
			in:   "line one<br />line two",
			want: "line one\nline two",
		},
		{
			name: "void br tag",
			in:   "line one<br>line two",
			want: "line one\nline two",
		},
		{
			name: "compact self-closing br tag",
			in:   "a<br/>b",
			want: "a\nb",
		},
		{
			name: "extra whitespace inside tag",
			in:   "a<br   />b",
			want: "a\nb",
		},
		{
			name: "case insensitive",
			in:   "a<BR>b<Br/>c",
			want: "a\nb\nc",
		},
		{
			name: "georgian content with PHP nl2br output",
			in:   "ცოფის ვაქცინა გაკეთდა.<br />შემდეგი ვიზიტი 1 თვეში.<br />ცოფი არ აღინიშნება.",
			want: "ცოფის ვაქცინა გაკეთდა.\nშემდეგი ვიზიტი 1 თვეში.\nცოფი არ აღინიშნება.",
		},
		{
			name: "trailing whitespace per line trimmed",
			in:   "line one   \nline two\t",
			want: "line one\nline two",
		},
		{
			name: "leading whitespace preserved (intentional indent)",
			in:   "header\n  indented",
			want: "header\n  indented",
		},
		{
			name: "runs of 3+ blank lines collapse to 2",
			in:   "a\n\n\n\n\nb",
			want: "a\n\nb",
		},
		{
			name: "outer whitespace trimmed",
			in:   "   <br />   surrounded   <br />   ",
			want: "surrounded",
		},
		{
			name: "idempotent — running twice gives the same result",
			in:   "a<br />b<br />c",
			want: "a\nb\nc",
		},
		{
			name: "mix of br and real newlines",
			in:   "a<br />b\nc<br/>d",
			want: "a\nb\nc\nd",
		},
		{
			name: "PHP Windows-style: <br /> followed by \\r\\n collapses to single \\n",
			in:   "line one<br />\r\nline two",
			want: "line one\nline two",
		},
		{
			name: "PHP Unix-style: <br /> followed by \\n collapses to single \\n",
			in:   "line one<br />\nline two",
			want: "line one\nline two",
		},
		{
			name: "stacked <br /><br /> for paragraph break preserved",
			in:   "para1<br /><br />para2",
			want: "para1\n\npara2",
		},
		{
			name: "real PHP-stored sample with <br />\\r\\n sequences",
			in:   "ცოფის ვაქცინა გაკეთდა.<br />\r\nშემდეგი ვიზიტი 1 თვეში.<br />\r\nცოფი არ აღინიშნება.",
			want: "ცოფის ვაქცინა გაკეთდა.\nშემდეგი ვიზიტი 1 თვეში.\nცოფი არ აღინიშნება.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanText(tc.in)
			if got != tc.want {
				t.Errorf("cleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Idempotency check: running cleanText on its own output
			// must be a no-op for every test case.
			if again := cleanText(got); again != got {
				t.Errorf("cleanText not idempotent: cleanText(cleanText(%q)) = %q, want %q",
					tc.in, again, got)
			}
		})
	}
}

func TestOnlyDigits(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"0", true},
		{"1", true},
		{"107", true},
		{"1a", false},
		{"a1", false},
		{" 1 ", false}, // whitespace not allowed
		{"1.5", false},
		{"-1", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := onlyDigits(tc.in); got != tc.want {
				t.Errorf("onlyDigits(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeTPName(t *testing.T) {
	cases := []struct {
		name string
		tp   int
		raw  string
		want string
	}{
		{
			name: "known tp returns canonical Georgian label",
			tp:   1,
			raw:  "anything",
			want: "ვაქცინაცია",
		},
		{
			name: "known tp wins over raw even when raw is the right label",
			tp:   1,
			raw:  "ვაქცინაცია",
			want: "ვაქცინაცია",
		},
		{
			name: "ectoparasite tp=11 mapped correctly",
			tp:   11,
			raw:  "",
			want: "ექტოპარაზიტების პრევენცია",
		},
		{
			name: "dehelminization tp=12 mapped correctly",
			tp:   12,
			raw:  "",
			want: "დეჰელმინთიზაცია",
		},
		{
			name: "test cat tp=22 mapped correctly",
			tp:   22,
			raw:  "ანალიზი",
			want: "ანალიზი (კატა)",
		},
		{
			name: "unknown tp with bare numeric raw value hidden",
			tp:   555,
			raw:  "1",
			want: "",
		},
		{
			name: "unknown tp with bare numeric raw value (multi-digit) hidden",
			tp:   555,
			raw:  "107",
			want: "",
		},
		{
			name: "unknown tp with empty raw returns empty",
			tp:   555,
			raw:  "",
			want: "",
		},
		{
			name: "unknown tp with whitespace-only raw returns empty",
			tp:   555,
			raw:  "   ",
			want: "",
		},
		{
			name: "unknown tp with real label preserved",
			tp:   555,
			raw:  "Mystery procedure",
			want: "Mystery procedure",
		},
		{
			name: "unknown tp with georgian raw preserved and trimmed",
			tp:   555,
			raw:  "  დაავადება  ",
			want: "დაავადება",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeTPName(tc.tp, tc.raw)
			if got != tc.want {
				t.Errorf("normalizeTPName(%d, %q) = %q, want %q", tc.tp, tc.raw, got, tc.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		opts []string
		want string
	}{
		{"all empty", []string{"", "", ""}, ""},
		{"first wins", []string{"a", "b", "c"}, "a"},
		{"falls through whitespace", []string{"   ", "b"}, "b"},
		{"preserves leading whitespace of returned value", []string{"", "  hello"}, "  hello"},
		{"no args", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := firstNonEmpty(tc.opts...)
			if got != tc.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.opts, got, tc.want)
			}
		})
	}
}
