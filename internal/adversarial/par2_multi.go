package adversarial

import (
	"bytes"
	"hash/crc32"
	"sort"
)

// PAR2's GF(2^16), polynomial x^16+x^12+x^3+x+1. Constants skip
// powers whose exponent is divisible by 3, 5, 17 or 257 (PAR2 specification).
func gfMultiply(a, b uint16) uint16 {
	var result uint16
	for b != 0 {
		if b&1 != 0 {
			result ^= a
		}
		high := a & 0x8000
		a <<= 1
		if high != 0 {
			a ^= 0x100b
		}
		b >>= 1
	}
	return result
}
func gfPower(a uint16, n uint32) uint16 {
	result := uint16(1)
	for n != 0 {
		if n&1 != 0 {
			result = gfMultiply(result, a)
		}
		a = gfMultiply(a, a)
		n >>= 1
	}
	return result
}
func parConstants(n int) []uint16 {
	out := make([]uint16, 0, n)
	for exp := uint32(1); len(out) < n; exp++ {
		if exp%3 != 0 && exp%5 != 0 && exp%17 != 0 && exp%257 != 0 {
			out = append(out, gfPower(2, exp))
		}
	}
	return out
}

func multiPAR2(files []namedBytes, sliceSize int, exponents []uint32) []byte {
	type source struct {
		namedBytes
		id []byte
	}
	var sources []source
	for _, file := range files {
		first := file.data[:min(len(file.data), 16384)]
		id := md5Bytes(bytes.Join([][]byte{md5Bytes(first), fixed64(uint64(len(file.data))), []byte(file.name)}, nil))
		sources = append(sources, source{file, id})
	}
	sort.Slice(sources, func(i, j int) bool {
		for k := 15; k >= 0; k-- {
			if sources[i].id[k] != sources[j].id[k] {
				return sources[i].id[k] < sources[j].id[k]
			}
		}
		return false
	})
	main := append(fixed64(uint64(sliceSize)), fixed32(uint32(len(sources)))...)
	for _, file := range sources {
		main = append(main, file.id...)
	}
	set := md5Bytes(main)
	packets := [][]byte{packet("Main", set, main)}
	var slices [][]byte
	for _, file := range sources {
		desc := bytes.Join([][]byte{file.id, md5Bytes(file.data), md5Bytes(file.data[:min(len(file.data), 16384)]), fixed64(uint64(len(file.data))), []byte(file.name)}, nil)
		checks := append([]byte{}, file.id...)
		for offset := 0; offset < len(file.data); offset += sliceSize {
			block := make([]byte, sliceSize)
			copy(block, file.data[offset:min(offset+sliceSize, len(file.data))])
			slices = append(slices, block)
			checks = append(checks, md5Bytes(block)...)
			checks = append(checks, fixed32(crc32.ChecksumIEEE(block))...)
		}
		packets = append(packets, packet("FileDesc", set, desc), packet("IFSC", set, checks))
	}
	constants := parConstants(len(slices))
	for _, exp := range exponents {
		recovery := make([]byte, sliceSize)
		for i, block := range slices {
			coefficient := gfPower(constants[i], exp)
			for offset := 0; offset < sliceSize; offset += 2 {
				value := le.Uint16(recovery[offset:]) ^ gfMultiply(le.Uint16(block[offset:]), coefficient)
				le.PutUint16(recovery[offset:], value)
			}
		}
		packets = append(packets, packet("RecvSlic", set, append(fixed32(exp), recovery...)))
	}
	packets = append(packets, packet("Creator", set, []byte("usenet-bench deterministic multislice generator")))
	return bytes.Join(packets, nil)
}

func multiRepair(b *Bundle, variant string) {
	alpha := make([]byte, 5*4096+23)
	beta := make([]byte, 2*4096+13)
	for i := range alpha {
		alpha[i] = byte(i*37 + i/257)
	}
	for i := range beta {
		beta[i] = byte(i*71 + i/113)
	}
	originals := []namedBytes{{"alpha.bin", alpha}, {"beta.bin", beta}}
	exponents := []uint32{0, 1, 2}
	switch variant {
	case "exactly-sufficient":
		exponents = []uint32{0, 1}
	case "insufficient":
		exponents = []uint32{0}
	case "dependent-equations":
		exponents = []uint32{0, 0}
	}
	par := multiPAR2(originals, 4096, exponents)
	damagedAlpha := append([]byte{}, alpha...)
	damagedBeta := append([]byte{}, beta...)
	damagedAlpha[16385] ^= 0xa5
	damagedBeta[len(beta)-1] ^= 0x5a
	b.post(namedBytes{"alpha.bin", damagedAlpha}, namedBytes{"beta.bin", damagedBeta}, namedBytes{"fixture.par2", par})
	if b.Manifest.Case.Expectation == Recover {
		b.Manifest.ExpectedOutputs = map[string]string{"alpha.bin": Digest(alpha), "beta.bin": Digest(beta)}
	}
}
