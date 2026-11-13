package fixture

// CorruptedArticleBytes is how many bytes a damaged article has changed: the
// same 128 bytes the byte-flip lanes change.
const CorruptedArticleBytes = 128

// CorruptArticleBytes is the range of an article of the given length that a
// damaged-article fault changes, relative to the article's first byte: 128
// bytes in its middle, away from both edges so the change never touches the
// article next to it.
func CorruptArticleBytes(articleLength int) (start, length int) {
	length = min(CorruptedArticleBytes, articleLength)
	start = (articleLength - length) / 2
	return start, length
}

// CorruptArticle changes an article's decoded bytes in place, the way every
// damaged-article fault changes them. The generator applies it to the file
// for a silent fault and to its repair-verification copy for a corrupt one,
// and the seeder applies it to the article it posts damaged, so all three
// agree on the bytes a client receives.
func CorruptArticle(article []byte) {
	start, length := CorruptArticleBytes(len(article))
	for index := range length {
		article[start+index] ^= byte(0xa5 + index)
	}
}
