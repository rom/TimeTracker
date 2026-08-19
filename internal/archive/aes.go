// Package archive writes and reads the zip files this application produces,
// with optional AES encryption.
//
// The encryption is the WinZip AE-2 scheme, which is what 7-Zip, WinZip,
// Windows' own tooling via those, and most archive managers on Linux already
// understand. That interoperability is the entire point: a backup nobody can
// open without this binary is not a backup, it is a hostage. The alternative -
// wrapping the archive in a format of our own - would have been less code and
// considerably less useful.
//
// Legacy "ZipCrypto" is deliberately not implemented. It is broken by a known
// plaintext attack that takes seconds, and offering it would be worse than
// offering nothing because it looks like protection.
//
// See docs/adr/0030-encrypted-backup-archives.md.
package archive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

// The AE-2 constants, from the WinZip AES specification.
const (
	// aesMethod is the compression method an encrypted entry declares. The real
	// method lives in the extra field instead.
	aesMethod uint16 = 99
	// aesExtraID is the header ID of that extra field.
	aesExtraID uint16 = 0x9901
	// aesVendor is "AE", little-endian, as the specification writes it.
	aesVendor uint16 = 0x4541
	// aeVersion2 is AE-2, which sets the CRC to zero and relies on the
	// authentication code instead. AE-1 keeps the CRC, which leaks a checksum
	// of the plaintext - a real distinguisher when the plaintext is one of a
	// small set of candidates.
	aeVersion2 uint16 = 2
	// strength256 selects AES-256. The specification also allows 128 and 192;
	// there is no reason to offer a weaker one for a file this size.
	strength256 byte = 3

	// saltLength, keyLength and macKeyLength are what strength256 implies.
	saltLength    = 16
	keyLength     = 32
	macKeyLength  = 32
	verifierBytes = 2
	// authCodeLength is the truncated HMAC that follows the ciphertext.
	authCodeLength = 10
	// kdfIterations is fixed by the specification at 1000. It is low by modern
	// standards and cannot be raised without producing archives no other tool
	// can read, which is the trade this format is for. The advice to users is
	// therefore about the password, not the iteration count.
	kdfIterations = 1000
)

// ErrWrongPassword is returned when the password does not open an entry.
var ErrWrongPassword = errors.New("the password is wrong")

// ErrCorrupt is returned when an entry's authentication code does not match.
var ErrCorrupt = errors.New("the archive is damaged or has been tampered with")

// aesKeys are the three values derived from a password and a salt.
type aesKeys struct {
	encryption []byte
	mac        []byte
	// verifier is two bytes stored in the clear, so a reader can reject a wrong
	// password immediately instead of decrypting a whole file to discover it.
	// It is not a security check - two bytes agree by chance once in 65536 -
	// and the authentication code is what actually decides.
	verifier []byte
}

// deriveKeys runs the key derivation the specification mandates.
func deriveKeys(password string, salt []byte) aesKeys {
	material := pbkdf2.Key([]byte(password), salt, kdfIterations,
		keyLength+macKeyLength+verifierBytes, sha1.New)
	return aesKeys{
		encryption: material[:keyLength],
		mac:        material[keyLength : keyLength+macKeyLength],
		verifier:   material[keyLength+macKeyLength:],
	}
}

// aesExtraField builds the 0x9901 field that says how an entry is encrypted.
func aesExtraField(realMethod uint16) []byte {
	field := make([]byte, 11)
	binary.LittleEndian.PutUint16(field[0:], aesExtraID)
	binary.LittleEndian.PutUint16(field[2:], 7) // the payload length
	binary.LittleEndian.PutUint16(field[4:], aeVersion2)
	binary.LittleEndian.PutUint16(field[6:], aesVendor)
	field[8] = strength256
	binary.LittleEndian.PutUint16(field[9:], realMethod)
	return field
}

// parseAESExtra finds the encryption field in an entry's extra data.
//
// The extra area is a sequence of length-prefixed records, and any of them may
// be one this package does not know about, so it is walked rather than assumed
// to hold one field.
func parseAESExtra(extra []byte) (realMethod uint16, ok bool) {
	for len(extra) >= 4 {
		id := binary.LittleEndian.Uint16(extra)
		size := int(binary.LittleEndian.Uint16(extra[2:]))
		if len(extra) < 4+size {
			return 0, false
		}
		payload := extra[4 : 4+size]
		if id == aesExtraID && size >= 7 {
			if binary.LittleEndian.Uint16(payload[2:]) != aesVendor {
				return 0, false
			}
			if payload[4] != strength256 {
				// 128- and 192-bit archives are legal AE-2 and this package
				// simply does not write them. Saying so beats decrypting with
				// the wrong key length and reporting corruption.
				return 0, false
			}
			return binary.LittleEndian.Uint16(payload[5:]), true
		}
		extra = extra[4+size:]
	}
	return 0, false
}

// aesCTR builds the counter-mode stream the format specifies.
//
// The counter is little-endian and starts at one, which is what makes this
// incompatible with crypto/cipher's own CTR mode - that one counts big-endian.
// Writing it out rather than reaching for cipher.NewCTR is the whole of the
// difference, and getting it backwards produces an archive that looks fine and
// no other tool can read.
type aesCTR struct {
	block   cipher.Block
	counter uint64
	// keystream holds the unused tail of the current block, so a write that
	// does not land on a 16-byte boundary does not restart the counter.
	keystream []byte
}

func newAESCTR(key []byte) (*aesCTR, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &aesCTR{block: block, counter: 1}, nil
}

// XOR applies the keystream to data in place.
func (c *aesCTR) XOR(data []byte) {
	for len(data) > 0 {
		if len(c.keystream) == 0 {
			var counterBlock [aes.BlockSize]byte
			binary.LittleEndian.PutUint64(counterBlock[:8], c.counter)
			// The upper half stays zero: no entry this application writes comes
			// anywhere near 2^64 blocks, and the specification counts the whole
			// 16 bytes little-endian regardless.
			block := make([]byte, aes.BlockSize)
			c.block.Encrypt(block, counterBlock[:])
			c.keystream = block
			c.counter++
		}
		n := min(len(data), len(c.keystream))
		for i := range n {
			data[i] ^= c.keystream[i]
		}
		data = data[n:]
		c.keystream = c.keystream[n:]
	}
}

// encryptAE2 turns compressed bytes into an AE-2 entry body.
//
// The layout is salt, password verifier, ciphertext, then the truncated HMAC of
// the ciphertext. Encrypt-then-MAC, as the specification requires and as one
// would choose anyway.
func encryptAE2(password string, compressed []byte) ([]byte, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	keys := deriveKeys(password, salt)

	stream, err := newAESCTR(keys.encryption)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, len(compressed))
	copy(ciphertext, compressed)
	stream.XOR(ciphertext)

	mac := hmac.New(sha1.New, keys.mac)
	mac.Write(ciphertext)

	body := make([]byte, 0, len(salt)+verifierBytes+len(ciphertext)+authCodeLength)
	body = append(body, salt...)
	body = append(body, keys.verifier...)
	body = append(body, ciphertext...)
	body = append(body, mac.Sum(nil)[:authCodeLength]...)
	return body, nil
}

// decryptAE2 reverses encryptAE2, returning the compressed bytes.
func decryptAE2(password string, body []byte) ([]byte, error) {
	const overhead = saltLength + verifierBytes + authCodeLength
	if len(body) < overhead {
		return nil, ErrCorrupt
	}

	salt := body[:saltLength]
	verifier := body[saltLength : saltLength+verifierBytes]
	ciphertext := body[saltLength+verifierBytes : len(body)-authCodeLength]
	authCode := body[len(body)-authCodeLength:]

	keys := deriveKeys(password, salt)
	// Constant time, because this comparison runs against an attacker-supplied
	// archive and there is no reason to leak how far a guess got.
	if subtle.ConstantTimeCompare(verifier, keys.verifier) != 1 {
		return nil, ErrWrongPassword
	}

	mac := hmac.New(sha1.New, keys.mac)
	mac.Write(ciphertext)
	if !hmac.Equal(mac.Sum(nil)[:authCodeLength], authCode) {
		// The verifier agreed and the code did not. Either the password is one
		// of the 1-in-65536 that pass the two-byte check, or the file is
		// damaged; the honest answer names both.
		return nil, fmt.Errorf("%w (or the password is wrong)", ErrCorrupt)
	}

	stream, err := newAESCTR(keys.encryption)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	copy(plaintext, ciphertext)
	stream.XOR(plaintext)
	return plaintext, nil
}

// readAll reads r with a ceiling, so a hostile archive cannot exhaust memory.
func readAll(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("an entry is larger than the %d byte limit", limit)
	}
	return data, nil
}
