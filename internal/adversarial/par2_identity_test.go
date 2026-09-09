package adversarial

import (
	"bytes"
	"strings"
	"testing"
)

func TestPAR2MetadataMutationsPreserveIdentityEnvelope(t *testing.T) {
	const name = "payload.bin"
	data := []byte("independent protected bytes")
	for _, variant := range []string{"consistent-file-length-overflow", "consistent-zero-file-length", "consistent-first16k-mismatch"} {
		t.Run(variant, func(t *testing.T) {
			raw := par2(name, data, variant)
			packets := map[string][]byte{}
			var set []byte
			for len(raw) > 0 {
				if len(raw) < 64 || string(raw[:8]) != "PAR2\x00PKT" {
					t.Fatal("broken packet envelope")
				}
				n := le.Uint64(raw[8:16])
				if n < 64 || n%4 != 0 || n > uint64(len(raw)) {
					t.Fatal("broken packet size")
				}
				p := raw[:n]
				raw = raw[n:]
				if !bytes.Equal(p[16:32], md5Bytes(p[32:])) {
					t.Fatal("packet MD5 mismatch")
				}
				if set == nil {
					set = p[32:48]
				} else if !bytes.Equal(set, p[32:48]) {
					t.Fatal("foreign set")
				}
				packets[strings.TrimRight(string(p[56:64]), "\x00")] = p[64:]
			}
			main, desc, ifsc := packets["Main"], packets["FileDesc"], packets["IFSC"]
			if len(main) != 28 || len(desc) < 56+len(name) || len(ifsc) != 36 {
				t.Fatal("unexpected packet topology")
			}
			if !bytes.Equal(set, md5Bytes(main)) {
				t.Fatal("Main set identity stale")
			}
			id := md5Bytes(desc[32 : 56+len(name)])
			if !bytes.Equal(desc[:16], id) || !bytes.Equal(main[12:28], id) || !bytes.Equal(ifsc[:16], id) {
				t.Fatal("File ID/references stale")
			}
			if !bytes.Equal(desc[16:32], md5Bytes(data)) {
				t.Fatal("unrelated full-file digest changed")
			}
			switch variant {
			case "consistent-file-length-overflow":
				if le.Uint64(desc[48:56]) != ^uint64(0) {
					t.Fatal("overflow mutation absent")
				}
			case "consistent-zero-file-length":
				if le.Uint64(desc[48:56]) != 0 {
					t.Fatal("zero-length mutation absent")
				}
			case "consistent-first16k-mismatch":
				if bytes.Equal(desc[32:48], md5Bytes(data)) {
					t.Fatal("hash mutation absent")
				}
			}
			bundle, err := Generate("par2-" + variant)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(bundle.Manifest.Contract.PreservedEnvelope, "recomputed consistently") {
				t.Fatal("missing declared identity preservation")
			}
		})
	}
}
