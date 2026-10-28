package sqlitelocal

import "encoding/hex"

// keySpec renders a raw key in SQLCipher's raw-key blob format, `x'<hex>'`, which tells SQLCipher to use the
// bytes as the encryption key directly.
//
// The encoding is load-bearing, not cosmetic. SQLCipher recognises a raw key only by this exact shape: it
// must start with `x'`, end with `'`, and hold an even number of hex digits in between. A key handed over as
// plain bytes fails that test and is treated as a passphrase, so SQLCipher stretches it with 256,000
// iterations of PBKDF2-HMAC-SHA512 — roughly 50ms on a fast laptop and several hundred milliseconds on a
// throttled container, paid on every single connection open.
//
// That stretching is worthless here. Key stretching exists to make brute-forcing a low-entropy human
// passphrase expensive; it adds security only when the input carries less entropy than the output. These
// keys are 32 bytes straight from crypto/rand, so stretching 256 bits of random material yields 256 bits of
// random material and nothing more. It does not slow down a real attacker either — anyone holding the
// wrapped key and the KEK pays the derivation once, exactly like the platform does.
func keySpec(key []byte) []byte {
	spec := make([]byte, 0, len("x''")+hex.EncodedLen(len(key)))
	spec = append(spec, 'x', '\'')
	spec = hex.AppendEncode(spec, key)
	return append(spec, '\'')
}
