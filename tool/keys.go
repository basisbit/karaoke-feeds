package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

func pubString(pub ed25519.PublicKey) string {
	return "ed25519:" + base64.RawURLEncoding.EncodeToString(pub)
}

// publicKeyArg reads the value of a --key option that expects a public key: either the key itself
// ("ed25519:...", as printed by kfeed keygen and kfeed pubkey) or a file containing it.
func publicKeyArg(s string) (string, error) {
	if strings.HasPrefix(s, "ed25519:") {
		_, err := parsePub(s)
		return s, err
	}
	data, err := os.ReadFile(s)
	if err != nil {
		return "", fmt.Errorf("--key: %q is neither a public key (ed25519:...) nor a readable file: %v", s, err)
	}
	if strings.Contains(string(data), "PRIVATE KEY") {
		return "", fmt.Errorf("--key: %s is a private key; here the public key is needed. Make a file with it: kfeed pubkey %s > feed.pub", s, s)
	}
	key := strings.TrimSpace(string(data))
	_, err = parsePub(key)
	return key, err
}

func parsePub(s string) (ed25519.PublicKey, error) {
	rest, ok := strings.CutPrefix(s, "ed25519:")
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if !ok || err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%q is not a public key of the form ed25519:<43 characters>", s)
	}
	return ed25519.PublicKey(b), nil
}

// stdin is where "--key -" reads from; tests replace it.
var stdin io.Reader = os.Stdin

// loadKey reads a private key: a PKCS#8 PEM file (the format `kfeed keygen` and `openssl genpkey -algorithm ed25519`
// write), or standard input when path is "-", so the key can come from a password manager or a CI secret without
// ever being stored unencrypted. Like OpenSSH, it refuses a key file that other users can read.
func loadKey(path string) (ed25519.PrivateKey, error) {
	var data []byte
	if path == "-" {
		var err error
		if data, err = io.ReadAll(io.LimitReader(stdin, 64<<10)); err != nil {
			return nil, fmt.Errorf("reading the key from standard input: %v", err)
		}
		path = "standard input"
	} else {
		fi, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s can be read by other users (permissions %v). Private keys must be readable only by you: chmod 600 %s", path, fi.Mode().Perm(), path)
		}
		if data, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s is not a PEM key file", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an Ed25519 key", path)
	}
	return priv, nil
}

// sigValid reports whether sigText (base64url, as stored in sig/<hash>.sig) is a signature of msg by any of keys.
func sigValid(keys []string, msg []byte, sigText string) bool {
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(sigText))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	for _, k := range keys {
		if pub, err := parsePub(k); err == nil && ed25519.Verify(pub, msg, sig) {
			return true
		}
	}
	return false
}

func cmdKeygen(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: kfeed keygen <key-file>")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // never overwrites an existing key
	if err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("Private key written to %s. Keep it secret and keep a backup.\n", args[0])
	fmt.Printf("Public key (add it to \"keys\" in feed-config.json):\n%s\n", pubString(pub))
	return nil
}

func cmdPubkey(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: kfeed pubkey <key-file>   (or - to read the key from standard input)")
	}
	priv, err := loadKey(args[0])
	if err != nil {
		return err
	}
	fmt.Println(pubString(priv.Public().(ed25519.PublicKey)))
	return nil
}
