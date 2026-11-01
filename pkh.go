package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
)

// The HOME container (PKH), as documented by PKHeX's PKHeX.Core/PKM/HOME/HomeCrypto.cs (facts only,
// no code copied): a 16-byte clear header (u16 version 1..4, u64 seed, u32 checksum, u16 body size)
// and an AES-128-CBC body whose key and IV derive from the seed. Not verified against a real HOME
// file (none in the dump set): TODO(capture) confirm the masks and padding on a captured PKH.

const pkhHeader = 0x10

func pkhKeyIV(seed uint64) (key, iv []byte) {
	key, iv = make([]byte, 16), make([]byte, 16)
	binary.BigEndian.PutUint64(key[0:], seed^0x6B7B5966193DB88B)
	binary.BigEndian.PutUint64(key[8:], seed&0x937EC53BF8856E87)
	binary.BigEndian.PutUint64(iv[0:], seed^0x5F4ED4E84975D976)
	binary.BigEndian.PutUint64(iv[8:], seed|0xE3CDA917EA9E489C)
	return
}

// pkhCrypt decrypts (or encrypts) the body in place of a copy; the header is kept.
func pkhCrypt(b []byte, encrypt bool) ([]byte, error) {
	if len(b) < pkhHeader+16 {
		return nil, errors.New("too short")
	}
	if v := binary.LittleEndian.Uint16(b); v < 1 || v > 4 {
		return nil, errors.New("unknown version")
	}
	body := b[pkhHeader:]
	if len(body)%16 != 0 {
		return nil, errors.New("body is not block aligned")
	}
	key, iv := pkhKeyIV(binary.LittleEndian.Uint64(b[2:]))
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), b...)
	if encrypt {
		cipher.NewCBCEncrypter(blk, iv).CryptBlocks(out[pkhHeader:], body)
	} else {
		cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out[pkhHeader:], body)
	}
	return out, nil
}

// PKHPlain returns the container with its body decrypted, the form the legality service opens.
// TODO(capture): detect an already-plain container (PKHeX tests string terminators) and the
// checksum at 0x0A; for now the wire form is assumed encrypted.
func PKHPlain(b []byte) ([]byte, error) { return pkhCrypt(b, false) }

// PKHWrap re-encrypts a plain container for sending to a client.
func PKHWrap(b []byte) ([]byte, error) { return pkhCrypt(b, true) }
