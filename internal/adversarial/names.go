package adversarial

import "strings"

func caseDisplayName(c Case) string {
	family := map[string]string{"control": "Valid input control", "nzb": "NZB parser", "yenc": "yEnc decoder", "yenc-boundary": "yEnc decoder boundary", "assembly": "Multipart assembly", "nntp": "NNTP response handling", "zip": "ZIP archive", "tar": "TAR archive", "rar4": "RAR4 archive", "rar5": "RAR5 archive", "7z": "7-Zip archive", "par2": "PAR2 metadata", "repair": "PAR2 recovery", "repair-multi": "Multi-file PAR2 recovery", "gzip": "gzip stream", "deflate": "Deflate stream", "uu": "uuencode decoder", "sfv": "SFV checksum list", "chain": "Cross-component input", "exploit-chain": "Exploit primitive chain", "historical-regression": "Published security advisory regression"}[c.Family]
	if strings.HasSuffix(c.Family, "-path") {
		base := strings.TrimSuffix(c.Family, "-path")
		family = map[string]string{"zip": "ZIP", "tar": "TAR", "rar4": "RAR4", "rar5": "RAR5", "7z": "7-Zip", "par2": "PAR2", "yenc": "yEnc", "nzb": "NZB"}[base] + " filename safety"
	}
	vector := c.Variant
	if c.Family == "yenc-boundary" {
		return family + " — " + vector + " decoded bytes"
	}
	phrases := map[string]string{"rtt": "round-trip latency", "parent": "parent-directory traversal", "nested-parent": "nested parent-directory traversal", "absolute": "absolute filesystem path", "ads": "Windows alternate data stream", "unc": "Windows network share path", "bidi": "bidirectional text controls", "nul": "embedded NUL byte", "crlf-message-id": "line-break injection in message ID", "bad-part-crc": "incorrect part checksum", "bad-crc": "incorrect checksum", "external-file-entity": "external XML entity reading a file", "external-http-entity": "external XML entity making an HTTP request", "quick-open-conflict": "malformed quick-open service record", "encoded-header": "encoded-header marker without a complete encoded header", "prefix-polyglot": "inert bytes before archive signature", "filename-count-lie": "inconsistent filename identity", "400-without-close": "temporary failure without closing the connection", "temporary-failure": "temporary failure followed by connection close", "dependent-equations": "duplicate, dependent recovery equations", "exactly-sufficient": "exactly sufficient recovery data", "surplus-recovery": "surplus recovery data", "unique-files": "many distinct file entries", "unique-segments": "many distinct article identifiers", "sab-cve-2021-29488-par2-parent-escape": "CVE-2021-29488 direct PAR2 parent-directory escape", "sab-ghsa-75g3-unpacked-par2-parent-escape": "GHSA-75g3 archive-extracted PAR2 parent-directory escape", "sab-ghsa-mjwj-symlink-dotdot-par2-escape": "GHSA-mjwj symlink and PAR2 parent-directory escape"}
	if phrase, ok := phrases[vector]; ok {
		vector = phrase
	} else {
		vector = strings.ReplaceAll(vector, "-", " ")
		vector = strings.NewReplacer("crc", "checksum", "eocd", "end-of-directory record", "ifsc", "input-file slice checksums", "vint", "variable-length integer", "utf8", "UTF-8", "kdf", "key-derivation work factor").Replace(vector)
	}
	return family + " — " + vector
}
