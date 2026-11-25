package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Workspace secrets, such as the FIRMS map key, are sealed with AES-256-GCM.
// The 32-byte key comes from FIMRA_SETTINGS_KEY and never reaches the
// database. Each row records the ID of the key that sealed it, so older keys
// listed in FIMRA_SETTINGS_OLD_KEYS can still open rows until
// `rotate-settings-key` seals them again with the current key.

const firmsSecret = "firms_map_key"

var errSecretsOff = errors.New("encrypted settings are off: set FIMRA_SETTINGS_KEY to a base64-encoded 32-byte key")

type settingsCipher struct {
	currentID string
	keys      map[string]cipher.AEAD
}

type sealedSecret struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

func readSettingsCipher() (*settingsCipher, error) {
	return newSettingsCipher(os.Getenv("FIMRA_SETTINGS_KEY"), os.Getenv("FIMRA_SETTINGS_OLD_KEYS"))
}

// newSettingsCipher returns nil, without an error, when no current key is set.
func newSettingsCipher(current, previous string) (*settingsCipher, error) {
	current = strings.TrimSpace(current)
	if current == "" {
		if strings.TrimSpace(previous) != "" {
			return nil, errors.New("FIMRA_SETTINGS_OLD_KEYS needs a current FIMRA_SETTINGS_KEY")
		}
		return nil, nil
	}
	c := &settingsCipher{keys: map[string]cipher.AEAD{}}
	id, aead, err := settingsKey(current)
	if err != nil {
		return nil, fmt.Errorf("FIMRA_SETTINGS_KEY: %w", err)
	}
	c.currentID = id
	c.keys[id] = aead
	for i, raw := range strings.Split(previous, ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		id, aead, err := settingsKey(raw)
		if err != nil {
			return nil, fmt.Errorf("FIMRA_SETTINGS_OLD_KEYS entry %d: %w", i+1, err)
		}
		c.keys[id] = aead
	}
	return c, nil
}

func settingsKey(encoded string) (string, cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, errors.New("the key is not valid base64")
	}
	if len(key) != 32 {
		return "", nil, fmt.Errorf("the key must decode to 32 bytes, not %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, err
	}
	// The ID names the key without revealing it.
	digest := sha256.Sum256(append([]byte("open-fimra settings key\x00"), key...))
	return hex.EncodeToString(digest[:8]), aead, nil
}

// The associated data binds a sealed value to its workspace and name, so a
// ciphertext copied to another row does not open.
func secretContext(workspaceID, name string) []byte {
	return []byte("open-fimra:" + workspaceID + ":" + name)
}

func (c *settingsCipher) seal(workspaceID, name, value string) (sealedSecret, error) {
	if c == nil {
		return sealedSecret{}, errSecretsOff
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return sealedSecret{}, fmt.Errorf("read a random nonce: %w", err)
	}
	aead := c.keys[c.currentID]
	return sealedSecret{
		KeyID: c.currentID, Nonce: nonce,
		Ciphertext: aead.Seal(nil, nonce, []byte(value), secretContext(workspaceID, name)),
	}, nil
}

func (c *settingsCipher) open(workspaceID, name string, s sealedSecret) (string, error) {
	if c == nil {
		return "", errSecretsOff
	}
	aead, ok := c.keys[s.KeyID]
	if !ok {
		return "", fmt.Errorf("the %s secret was sealed with settings key %s, which is not configured; "+
			"add that key to FIMRA_SETTINGS_OLD_KEYS", name, s.KeyID)
	}
	plain, err := aead.Open(nil, s.Nonce, s.Ciphertext, secretContext(workspaceID, name))
	if err != nil {
		return "", fmt.Errorf("the %s secret could not be decrypted with settings key %s: wrong key or changed data", name, s.KeyID)
	}
	return string(plain), nil
}

// secretHint returns at most the last four characters of a secret.
func secretHint(value string) string {
	if utf8.RuneCountInString(value) < 12 {
		return ""
	}
	runes := []rune(value)
	return string(runes[len(runes)-4:])
}

type secretStatus struct {
	Set   bool       `json:"set"`
	SetAt *time.Time `json:"setAt,omitempty"`
	Hint  string     `json:"hint,omitempty"`
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func saveSecret(ctx context.Context, tx pgx.Tx, c *settingsCipher, workspaceID, name, value, actor string) error {
	sealed, err := c.seal(workspaceID, name, value)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO workspace_settings_secrets (workspace_id, name, key_id, nonce, ciphertext, hint, set_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (workspace_id, name) DO UPDATE SET key_id = EXCLUDED.key_id, nonce = EXCLUDED.nonce,
			ciphertext = EXCLUDED.ciphertext, hint = EXCLUDED.hint, set_at = now(), set_by = EXCLUDED.set_by
	`, workspaceID, name, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, secretHint(value), actor)
	if err != nil {
		return fmt.Errorf("save the %s secret: %w", name, err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
		VALUES ($1, 'user', $2, 'settings.secret_set', 'workspace_secret', $3, jsonb_build_object('keyId', $4::text))
	`, workspaceID, actor, name, sealed.KeyID)
	if err != nil {
		return fmt.Errorf("audit the %s secret: %w", name, err)
	}
	return nil
}

// loadSecret returns the opened secret, or found = false when none is saved.
func loadSecret(ctx context.Context, db rowQuerier, c *settingsCipher, workspaceID, name string) (value string, found bool, err error) {
	var sealed sealedSecret
	err = db.QueryRow(ctx, `
		SELECT key_id, nonce, ciphertext FROM workspace_settings_secrets WHERE workspace_id = $1 AND name = $2
	`, workspaceID, name).Scan(&sealed.KeyID, &sealed.Nonce, &sealed.Ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the %s secret: %w", name, err)
	}
	value, err = c.open(workspaceID, name, sealed)
	return value, true, err
}

func readSecretStatus(ctx context.Context, db rowQuerier, workspaceID, name string) (secretStatus, error) {
	var status secretStatus
	var setAt time.Time
	err := db.QueryRow(ctx, `
		SELECT set_at, hint FROM workspace_settings_secrets WHERE workspace_id = $1 AND name = $2
	`, workspaceID, name).Scan(&setAt, &status.Hint)
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("read the %s secret status: %w", name, err)
	}
	status.Set, status.SetAt = true, &setAt
	return status, nil
}

// rotateSecrets seals every secret that uses an older key with the current key.
func rotateSecrets(ctx context.Context, db ingestDB, c *settingsCipher) (int, error) {
	if c == nil {
		return 0, errSecretsOff
	}
	rotated := 0
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT workspace_id::text, name, key_id, nonce, ciphertext FROM workspace_settings_secrets
			WHERE key_id <> $1 ORDER BY workspace_id, name FOR UPDATE
		`, c.currentID)
		if err != nil {
			return fmt.Errorf("read secrets to rotate: %w", err)
		}
		type row struct {
			workspace, name string
			sealed          sealedSecret
		}
		pending, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var item row
			err := r.Scan(&item.workspace, &item.name, &item.sealed.KeyID, &item.sealed.Nonce, &item.sealed.Ciphertext)
			return item, err
		})
		if err != nil {
			return fmt.Errorf("collect secrets to rotate: %w", err)
		}
		for _, item := range pending {
			value, err := c.open(item.workspace, item.name, item.sealed)
			if err != nil {
				return fmt.Errorf("workspace %s: %w", item.workspace, err)
			}
			fresh, err := c.seal(item.workspace, item.name, value)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE workspace_settings_secrets SET key_id = $3, nonce = $4, ciphertext = $5
				WHERE workspace_id = $1 AND name = $2
			`, item.workspace, item.name, fresh.KeyID, fresh.Nonce, fresh.Ciphertext); err != nil {
				return fmt.Errorf("save the rotated %s secret: %w", item.name, err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO audit_log (workspace_id, actor_kind, action, target_type, target_id, details)
				VALUES ($1, 'system', 'settings.secret_rotate', 'workspace_secret', $2,
					jsonb_build_object('fromKeyId', $3::text, 'toKeyId', $4::text))
			`, item.workspace, item.name, item.sealed.KeyID, fresh.KeyID); err != nil {
				return fmt.Errorf("audit the rotated %s secret: %w", item.name, err)
			}
			rotated++
		}
		return nil
	})
	return rotated, err
}
