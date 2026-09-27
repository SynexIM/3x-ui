package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// ClientCredentialPatch changes only the fields it names; password also becomes
// the Hysteria2 auth, and empty mixed_user/mixed_pass fall back to email/password.
type ClientCredentialPatch struct {
	ID        *string `json:"id,omitempty" example:"0f7a8c1e-4d2b-4e6a-9c3f-1b2d3e4f5a6b"`
	Password  *string `json:"password,omitempty" example:"s3cret-Pass_01"`
	MixedUser *string `json:"mixed_user,omitempty" example:"line-0001"`
	MixedPass *string `json:"mixed_pass,omitempty" example:"s3cret-Pass_02"`
}

var (
	ErrClientCredentialInvalid  = errors.New("CLIENT_CREDENTIAL_INVALID")
	ErrClientCredentialConflict = errors.New("CLIENT_CREDENTIAL_CONFLICT")
)

func credentialInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrClientCredentialInvalid}, args...)...)
}

// UpdateCredentials rotates one client's credentials on every inbound it is
// attached to. Uniqueness is checked per inbound, where the core indexes them.
func (s *ClientService) UpdateCredentials(ctx context.Context, inboundSvc *InboundService, email string, patch ClientCredentialPatch) error {
	updates := map[string]any{}
	if patch.ID != nil {
		id, err := uuid.Parse(strings.TrimSpace(*patch.ID))
		if err != nil {
			return credentialInvalid("id must be a UUID")
		}
		updates["uuid"] = id.String()
	}
	if patch.Password != nil {
		if !validSecret(*patch.Password) {
			return credentialInvalid("password must be 1-128 printable characters without spaces")
		}
		updates["password"] = *patch.Password
		updates["auth"] = *patch.Password
	}
	if patch.MixedUser != nil {
		if *patch.MixedUser != "" && !validMixedUser(*patch.MixedUser) {
			return credentialInvalid("mixed_user must be 1-64 characters of A-Z a-z 0-9 . _ @ -")
		}
		updates["mixed_user"] = *patch.MixedUser
	}
	if patch.MixedPass != nil {
		if *patch.MixedPass != "" && !validSecret(*patch.MixedPass) {
			return credentialInvalid("mixed_pass must be 1-128 printable characters without spaces")
		}
		updates["mixed_pass"] = *patch.MixedPass
	}
	if len(updates) == 0 {
		return credentialInvalid("at least one of id, password, mixed_user, mixed_pass is required")
	}
	updates["updated_at"] = time.Now().UnixMilli()
	return s.updateClientRecordLive(ctx, inboundSvc, email, updates, func(tx *gorm.DB, record model.ClientRecord, inbounds []model.Inbound) error {
		return checkCredentialConflicts(tx, record, inbounds, updates)
	})
}

// checkCredentialConflicts refuses a credential another client on the same
// inbound already authenticates with; the core would reject or misroute it.
func checkCredentialConflicts(tx *gorm.DB, record model.ClientRecord, inbounds []model.Inbound, updates map[string]any) error {
	mixedUser, _ := updates["mixed_user"].(string)
	if _, set := updates["mixed_user"]; set && mixedUser == "" {
		mixedUser = record.Email
	}
	for _, ib := range inbounds {
		var column, value string
		switch ib.Protocol {
		case model.VLESS, model.VMESS:
			column, value = "c.uuid", stringUpdate(updates, "uuid")
		case model.Hysteria:
			column, value = "c.auth", stringUpdate(updates, "auth")
		case model.Shadowsocks:
			column, value = "c.password", stringUpdate(updates, "password")
		case model.Mixed:
			column, value = "COALESCE(NULLIF(c.mixed_user, ''), c.email)", mixedUser
		}
		if value == "" {
			continue
		}
		var taken int64
		if err := tx.Table("clients c").
			Joins("JOIN client_inbounds ci ON ci.client_id = c.id").
			Where("ci.inbound_id = ? AND c.id <> ? AND "+column+" = ?", ib.Id, record.Id, value).
			Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("%w: inbound %q already has a client with this credential", ErrClientCredentialConflict, ib.Tag)
		}
	}
	return nil
}

func stringUpdate(updates map[string]any, key string) string {
	v, _ := updates[key].(string)
	return v
}

func validSecret(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validMixedUser(v string) bool {
	if len(v) > 64 {
		return false
	}
	for _, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._@-", r)
		if !ok {
			return false
		}
	}
	return true
}
