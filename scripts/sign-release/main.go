// Command sign-release signs the release checksums file with the release
// Ed25519 key, producing the detached signature (checksums.txt.sig) the
// updater verifies before installing a downloaded archive.
//
//	OPENPANDA_RELEASE_KEY=<hex|base64 private key or 32-byte seed> \
//	  go run ./scripts/sign-release dist/checksums.txt > dist/checksums.txt.sig
//
//	OPENPANDA_RELEASE_KEY=<same> go run ./scripts/sign-release -pub
//	  # prints the hex public key to bake into the binary via
//	  # -X github.com/Xustalis/OpenPanda/internal/version.ReleasePubKey
//
//	OPENPANDA_RELEASE_PUBKEY=<hex|base64> \
//	  go run ./scripts/sign-release -verify dist/checksums.txt dist/checksums.txt.sig
//	  # OPENPANDA_RELEASE_KEY may replace _PUBKEY when the private half is
//	  # what the environment carries (the public key derives from it).
//
// The signature is emitted as a single base64 line — the updater's decoder
// (internal/updater decodeSignatureText) accepts base64 and hex, standard or
// URL-safe, padded or raw.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
)

// decodeSized tries each accepted encoding and returns the first decode that
// lands on exactly size bytes — a base64 seed that happens to be all-hex
// characters must not be claimed by the hex decoder just for decoding
// successfully.
func decodeSized(s string, size int) ([]byte, bool) {
	s = strings.TrimSpace(s)
	for _, dec := range []func(string) ([]byte, error){
		hex.DecodeString,
		base64.StdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == size {
			return b, true
		}
	}
	return nil, false
}

func decodeKey(s string) (ed25519.PrivateKey, error) {
	if b, ok := decodeSized(s, ed25519.PrivateKeySize); ok {
		return ed25519.PrivateKey(b), nil
	}
	if b, ok := decodeSized(s, ed25519.SeedSize); ok {
		return ed25519.NewKeyFromSeed(b), nil
	}
	return nil, fmt.Errorf("OPENPANDA_RELEASE_KEY is not a %d-byte private key or %d-byte seed (hex/base64)",
		ed25519.PrivateKeySize, ed25519.SeedSize)
}

func decodePub(s string) (ed25519.PublicKey, error) {
	b, ok := decodeSized(s, ed25519.PublicKeySize)
	if !ok {
		return nil, fmt.Errorf("OPENPANDA_RELEASE_PUBKEY is not a %d-byte public key (hex/base64)",
			ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// verifyKey returns the public key for -verify: OPENPANDA_RELEASE_PUBKEY
// when set, else the public half of OPENPANDA_RELEASE_KEY.
func verifyKey() (ed25519.PublicKey, error) {
	if s := os.Getenv("OPENPANDA_RELEASE_PUBKEY"); s != "" {
		return decodePub(s)
	}
	priv, err := decodeKey(os.Getenv("OPENPANDA_RELEASE_KEY"))
	if err != nil {
		return nil, err
	}
	return priv.Public().(ed25519.PublicKey), nil
}

func main() {
	pubOnly := flag.Bool("pub", false, "print the private key's public half (hex) and exit")
	verify := flag.Bool("verify", false, "verify <file> <sig-file> instead of signing")
	flag.Parse()

	if *verify {
		if flag.NArg() != 2 {
			fmt.Fprintln(os.Stderr, "usage: sign-release -verify <file> <sig-file>")
			os.Exit(2)
		}
		pub, err := verifyKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, "sign-release:", err)
			os.Exit(2)
		}
		data, err := os.ReadFile(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, "sign-release:", err)
			os.Exit(1)
		}
		sigText, err := os.ReadFile(flag.Arg(1))
		if err != nil {
			fmt.Fprintln(os.Stderr, "sign-release:", err)
			os.Exit(1)
		}
		sig, ok := decodeSized(string(sigText), ed25519.SignatureSize)
		if !ok {
			fmt.Fprintln(os.Stderr, "sign-release: signature file is not a 64-byte Ed25519 signature (hex/base64)")
			os.Exit(1)
		}
		if !ed25519.Verify(pub, data, sig) {
			fmt.Fprintln(os.Stderr, "sign-release: signature does not verify")
			os.Exit(1)
		}
		fmt.Println("signature OK")
		return
	}

	key, err := decodeKey(os.Getenv("OPENPANDA_RELEASE_KEY"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign-release:", err)
		os.Exit(2)
	}
	priv := ed25519.PrivateKey(key)

	if *pubOnly {
		fmt.Println(hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return
	}
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sign-release [-pub|-verify] <file-to-sign>")
		os.Exit(2)
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign-release:", err)
		os.Exit(1)
	}
	sig := ed25519.Sign(priv, data)
	fmt.Println(base64.StdEncoding.EncodeToString(sig))
}
