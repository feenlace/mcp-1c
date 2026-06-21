package dump

import "strings"

// Unicode NFC normalisation for 1C module names.
//
// 1C dumps unpacked on macOS carry their Russian object names in Unicode NFD
// (decomposed) form. The only two decomposable letters that occur in 1C Cyrillic
// identifiers are the short-I and IO letters (upper and lower case); every other
// Cyrillic letter and all ASCII-Latin used in identifiers is already atomic:
//
//	U+0419 (composed)  =  U+0418 + U+0306 (COMBINING BREVE)      // capital short I
//	U+0439 (composed)  =  U+0438 + U+0306                        // small short I
//	U+0401 (composed)  =  U+0415 + U+0308 (COMBINING DIAERESIS)  // capital IO
//	U+0451 (composed)  =  U+0435 + U+0308                        // small IO
//
// Queries (from the LLM/user) and names derived from XML content are NFC, so an
// NFD path-derived key never matches an NFC lookup (e.g. code_read resolve
// returns found:false for any name with a short-I/IO letter). Recomposing these
// four sequences to NFC is both necessary and exhaustive for 1C identifiers.
//
// A manual replacer is used deliberately instead of golang.org/x/text/unicode/norm:
// the four sequences above are the complete set for this domain, so norm's large
// Unicode tables (and their interaction with the release obfuscator) buy no
// correctness here.
//
// The code points are spelled as numeric constants (not Cyrillic glyphs) on
// purpose: the NFD and NFC forms render identically in an editor, so a raw glyph
// could be silently stored in the wrong normalisation and turn the replacer into
// a no-op. Building the sequences from explicit rune values pins the exact bytes
// regardless of how this source file is normalised on disk, and -- because there
// are no Cyrillic string literals -- leaves nothing for the release obfuscator's
// -literals pass to rewrite.
const (
	cyrCapI    = 0x0418 // И  base for capital short I
	cyrSmallI  = 0x0438 // и  base for small short I
	cyrCapIe   = 0x0415 // Е  base for capital IO
	cyrSmallIe = 0x0435 // е  base for small IO

	cyrCapShortI   = 0x0419 // Й  precomposed
	cyrSmallShortI = 0x0439 // й  precomposed
	cyrCapIo       = 0x0401 // Ё  precomposed
	cyrSmallIo     = 0x0451 // ё  precomposed

	// Combining marks; also used by the allocation-free fast-path guard.
	combiningBreve     = 0x0306 // COMBINING BREVE     -- short-I letters
	combiningDiaeresis = 0x0308 // COMBINING DIAERESIS -- IO letters
)

// nfcReplacer recomposes the four NFD sequences to their precomposed (NFC) form.
// Kept defensively excluded from the release obfuscator per convention (the
// numeric construction already makes it -literals-immune).
//
//garble:ignore
var nfcReplacer = strings.NewReplacer(
	string([]rune{cyrCapI, combiningBreve}), string(rune(cyrCapShortI)),
	string([]rune{cyrSmallI, combiningBreve}), string(rune(cyrSmallShortI)),
	string([]rune{cyrCapIe, combiningDiaeresis}), string(rune(cyrCapIo)),
	string([]rune{cyrSmallIe, combiningDiaeresis}), string(rune(cyrSmallIo)),
)

// NFC returns s with the two decomposable Cyrillic letters used in 1C
// identifiers (short-I and IO, upper and lower case) recomposed from their NFD
// form to NFC. Input that is already NFC -- the prod/HTTP/Windows and
// XML-content case -- is returned unchanged with no allocation via the
// fast-path guard.
func NFC(s string) string {
	// Allocation-free fast path: without either combining mark there is nothing
	// to recompose (ASCII and precomposed Cyrillic both take this path). This is
	// the common case, so NFC is effectively free on non-macOS input.
	if !strings.ContainsRune(s, combiningBreve) && !strings.ContainsRune(s, combiningDiaeresis) {
		return s
	}
	return nfcReplacer.Replace(s)
}
