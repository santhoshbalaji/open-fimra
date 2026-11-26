package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: verify <trusted-public-key.json> <signed-extract.json>")
	}
	keyRaw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var key struct {
		PublicKey []byte
		StateCode string
		KeyID     string
	}
	if err := json.Unmarshal(keyRaw, &key); err != nil || len(key.PublicKey) != ed25519.PublicKeySize ||
		key.StateCode == "" || key.KeyID != extract.KeyID(ed25519.PublicKey(key.PublicKey)) {
		return errors.New("invalid trusted public key file")
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	receipt, err := extract.Validate(raw, ed25519.PublicKey(key.PublicKey), time.Now())
	if err != nil {
		return err
	}
	var envelope struct {
		Payload extract.Document
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Payload.Authority.StateCode != key.StateCode {
		return errors.New("the extract is for a different state")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(receipt)
}
