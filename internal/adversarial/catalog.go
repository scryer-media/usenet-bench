package adversarial

import (
	"sort"
	"strconv"
)

// Catalog is executable: every entry dispatches to a generator. A catalog row
// without a working recipe is an error in the test suite, never a skipped case.
func Catalog() []Case {
	var cases []Case
	add := func(family, recipe string, outcome Expectation, variants ...string) {
		for _, v := range variants {
			cases = append(cases, Case{ID: family + "-" + v, Family: family, Vector: v, Expectation: outcome, Recipe: recipe, Variant: v})
		}
	}
	add("control", "control", Accept, "yenc", "multipart", "zip", "tar", "gzip", "deflate", "rar4", "rar5", "7z", "par2", "uuencode")
	add("nzb", "nzb", Reject, "empty", "truncated", "mismatched", "invalid-utf8", "no-segments")
	add("nzb", "nzb", Contain, "overflow-segment")
	add("nzb", "nzb", Contain, "nul", "trailing-data", "duplicate-attribute", "negative-bytes", "zero-bytes", "huge-bytes", "negative-number", "max-number", "duplicate-number", "duplicate-message-id", "sparse-number", "reverse-order", "missing-subject", "invalid-date", "empty-message-id", "crlf-message-id", "crlf-group", "duplicate-password", "foreign-namespace", "nested-file", "cdata", "huge-subject", "many-files", "many-segments", "deep-elements", "entity-recursion", "entity-expansion", "external-file-entity", "external-http-entity", "parameter-entity", "xinclude", "stylesheet", "bidi", "html", "shell", "terminal")
	add("yenc", "yenc", Reject, "missing-begin", "missing-end", "dangling-escape", "truncated-body", "bad-part-crc")
	add("yenc", "yenc", Contain, "missing-size", "negative-size", "overflow-size", "huge-size", "zero-size", "small-size", "large-size", "duplicate-size", "duplicate-begin", "duplicate-end", "zero-line", "huge-line", "unknown-field", "small-end-size", "large-end-size", "bad-crc", "missing-crc", "nonhex-crc", "conflicting-crcs", "part-zero", "part-overflow", "total-zero", "total-less-than-part", "begin-zero", "end-before-begin", "offset-overflow", "range-too-large", "range-too-small", "missing-part", "extra-body", "fake-trailer", "preamble", "trailing-data", "two-blocks", "lf-only", "cr-only", "all-bytes", "escape-heavy")
	add("assembly", "assembly", Contain, "reverse", "duplicate", "conflicting-duplicate", "overlap", "gap", "wrong-name", "wrong-total", "wrong-part", "missing-first", "missing-middle", "missing-last", "cross-file-id")
	add("nntp", "nntp", Contain, "short-status", "nonnumeric-status", "overlong-status", "wrong-status", "wrong-id", "missing-terminator", "early-terminator", "double-terminator", "disconnect", "truncated", "lf-only", "extra-response", "one-byte-chunks", "15-byte-chunks", "16-byte-chunks", "17-byte-chunks", "31-byte-chunks", "32-byte-chunks", "33-byte-chunks", "63-byte-chunks", "64-byte-chunks", "65-byte-chunks", "slow-body", "oversized-body", "first-missing", "first-corrupt", "always-missing", "temporary-failure", "auth-loop", "lying-capabilities", "status-html", "status-terminal")
	for _, family := range []string{"zip-path", "tar-path", "rar4-path", "rar5-path", "7z-path", "par2-path", "yenc-path", "nzb-path"} {
		for _, p := range pathVectors {
			cases = append(cases, Case{ID: family + "-" + p.name, Family: family, Vector: p.name, Expectation: Contain, Recipe: "path", Variant: p.name})
		}
	}
	add("zip", "zip", Contain, "bad-magic", "truncated", "bad-crc", "local-name-mismatch", "local-size-mismatch", "local-method-mismatch", "local-flag-mismatch", "size-overflow", "overlap-offset", "offset-beyond-eof", "missing-eocd", "duplicate-eocd", "zip64-without-record", "unknown-method", "duplicate-name", "file-directory-collision", "symlink", "unicode-override", "prefix-polyglot", "trailing-archive", "descriptor-mismatch", "output-bomb", "many-entries")
	add("tar", "tar", Contain, "bad-checksum", "truncated-header", "truncated-data", "size-lie", "negative-size", "base256-overflow", "missing-end", "trailing-archive", "duplicate-name", "file-directory-collision", "symlink", "hardlink", "parent-symlink", "link-chain", "fifo", "char-device", "block-device", "sparse", "pax-path", "pax-linkpath", "gnu-longname", "gnu-longlink", "privileged-mode", "hostile-owner", "hostile-time", "many-entries", "deep-path")
	add("rar4", "rar4", Contain, "bad-magic", "bad-header-crc", "truncated-header", "truncated-data", "header-size-lie", "packed-size-lie", "unpacked-size-lie", "bad-data-crc", "unknown-method", "split-before", "split-after", "solid-without-predecessor", "unknown-block", "high-dictionary")
	add("rar5", "rar5", Contain, "bad-magic", "bad-header-crc", "truncated-header", "truncated-data", "header-size-lie", "packed-size-lie", "unpacked-size-lie", "bad-data-crc", "unknown-method", "split-before", "split-after", "solid-without-predecessor", "unknown-block", "unterminated-vint", "overflow-vint", "extra-size-lie", "high-dictionary", "unknown-size", "kdf-extreme", "encryption-truncated", "quick-open-conflict", "symlink-record")
	add("7z", "7z", Contain, "bad-magic", "truncated-start", "bad-start-crc", "bad-next-crc", "next-offset-overflow", "next-offset-beyond-eof", "next-size-overflow", "overlapping-header", "truncated-data", "unpack-size-lie", "file-count-lie", "unknown-coder", "high-dictionary", "encoded-header", "cyclic-coder", "trailing-data")
	add("par2", "par2", Contain, "bad-magic", "truncated-header", "zero-length", "short-length", "unaligned-length", "overflow-length", "length-beyond-eof", "bad-packet-hash", "unknown-packet", "duplicate-packets", "conflicting-main", "reverse-packets", "junk-between-packets", "foreign-set", "zero-slice", "unaligned-slice", "huge-slice", "file-count-lie", "duplicate-file-id", "missing-main", "missing-description", "bad-file-id", "bad-full-hash", "bad-first16k-hash", "file-length-lie", "ifsc-short", "ifsc-extra", "ifsc-conflict", "recovery-short", "recovery-long", "duplicate-exponent", "conflicting-exponent", "max-exponent", "unicode-override", "many-packets", "many-sets", "filename-count-lie")
	add("repair", "repair", Recover, "one-slice", "missing-file", "exactly-sufficient", "damaged-recovery-packet", "renamed-file")
	add("repair", "repair", Reject, "insufficient", "lying-recovery")
	add("gzip", "gzip", Contain, "truncated", "bad-crc", "size-lie", "trailing-junk", "concatenated", "filename-traversal", "huge-comment", "output-bomb", "nested-zip")
	add("deflate", "deflate", Contain, "truncated", "invalid-block", "output-bomb")
	add("uu", "uu", Contain, "invalid-mode", "traversal-name", "invalid-length", "truncated-row", "missing-end", "nested-begin", "overlong-row", "mixed-yenc")
	add("sfv", "sfv", Contain, "bad-crc", "invalid-crc", "missing-file", "duplicate-conflict", "traversal", "absolute", "leading-option", "unicode-collision", "long-line", "shell", "html", "terminal")
	add("chain", "chain", Contain, "par2-renames-archive-escape", "zip-local-central-traversal", "nested-tar-link", "nested-gzip-bomb", "safe-member-before-traversal", "traversal-before-safe-member", "yenc-name-zip-traversal", "corrupt-first-unsafe-retry", "par2-shell-name", "stored-html-metadata", "archive-sfx-canary")
	add("exploit-chain", "chain", Contain, "zip-symlink-absolute-pivot", "zip-symlink-replacement-pivot", "tar-symlink-absolute-pivot", "tar-hardlink-overwrite-pivot", "tar-symlink-replacement-pivot", "nested-zip-tar-symlink-pivot", "nested-depth-tar-symlink-pivot", "nested-extension-magic-confusion", "reserved-staging-nested-archive", "shell-helper-archive-name", "shell-helper-member-name", "par2-helper-filename", "rar4-symlink-absolute-record", "rar5-symlink-absolute-record", "archive-option-filename", "parallel-zip-symlink-pivot", "parallel-tar-symlink-pivot", "parallel-cross-format-symlink-pivot", "parallel-file-directory-replacement")
	add("historical-regression", "chain", Contain, "sab-cve-2021-29488-par2-parent-escape", "sab-ghsa-75g3-unpacked-par2-parent-escape", "sab-ghsa-mjwj-symlink-dotdot-par2-escape")
	add("repair-multi", "repair-multi", Recover, "surplus-recovery", "exactly-sufficient")
	add("repair-multi", "repair-multi", Reject, "insufficient", "dependent-equations")
	add("par2", "par2", Contain, "consistent-file-length-overflow", "consistent-zero-file-length", "consistent-first16k-mismatch")
	add("nzb", "nzb", Contain, "unique-files", "unique-segments")
	add("nntp", "nntp", Contain, "400-without-close")
	for _, size := range []int{1, 127, 128, 129, 4095, 4096, 4097, 65535, 65536, 65537, 767999, 768000, 768001} {
		add("yenc-boundary", "yenc-boundary", Accept, strconv.Itoa(size))
	}
	renames := map[string]string{"rar5-quick-open-conflict": "rar5-malformed-quick-open-service", "7z-encoded-header": "7z-encoded-header-marker-only", "zip-prefix-polyglot": "zip-inert-prefix", "par2-filename-count-lie": "par2-inconsistent-filename-identity"}
	for i := range cases {
		if name, ok := renames[cases[i].ID]; ok {
			cases[i].ID = name
			cases[i].Vector = name
		}
		if cases[i].ID == "yenc-all-bytes" || cases[i].ID == "yenc-escape-heavy" {
			cases[i].Expectation = Accept
		}
		cases[i].DisplayName = caseDisplayName(cases[i])
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases
}

type pathVector struct{ name, value string }

var pathVectors = []pathVector{
	{"parent", "../escape.canary"},
	{"nested-parent", "safe/../../escape.canary"},
	{"absolute", "/adversarial-canary/escape.canary"},
	{"backslash", "..\\escape.canary"},
	{"mixed-separators", "safe\\../../escape.canary"},
	{"drive", "C:/adversarial-canary/escape.canary"},
	{"drive-relative", "C:escape.canary"},
	{"unc", "\\\\adversarial.invalid\\share\\escape.canary"},
	{"extended-windows", "\\\\?\\C:\\adversarial-canary\\escape.canary"},
	{"ads", "payload.bin:canary"},
	{"device", "CON"},
	{"nul", "safe.bin\x00../escape.canary"},
	{"newline", "safe\nFORGED-EVENT.canary"},
	{"trailing-dot", "payload.bin."},
	{"trailing-space", "payload.bin "},
	{"leading-option", "--help"},
	{"response-file", "@adversarial-canary"},
	{"shell", "$(adversarial-canary);`adversarial-canary`|adversarial-canary"},
	{"powershell", "$(adversarial-canary)&%ADVERSARIAL_CANARY%"},
	{"html", "<svg onload=alert('adversarial-canary')>.bin"},
	{"terminal", "\x1b]52;c;Y2FuYXJ5\x07.bin"},
	{"bidi", "payload.\u202eexe.bin"},
	{"unicode-nfd", "cafe\u0301.bin"},
	{"scratch-absolute", "/scratch/escape.canary"},
	{"posix-double-slash", "//scratch/escape.canary"},
	{"reserved-staging", ".weaver-staging/0/escape.canary"},
	{"reserved-direct-unpack", ".weaver-direct-unpack/0/escape.canary"},
	{"windows-globalroot", "\\\\?\\GLOBALROOT\\Device\\HarddiskVolumeShadowCopy1\\escape.canary"},
	{"windows-device-child", "CON/escape.canary"},
	{"shell-ifs", "$(adversarial-canary)"},
}

func pathValue(name string) string {
	for _, p := range pathVectors {
		if p.name == name {
			return p.value
		}
	}
	panic("unknown path recipe " + name)
}
