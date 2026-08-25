package panel

import (
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/util/random"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type ApiTokenService struct{}

const apiTokenLength = 48

type ApiTokenView struct {
	Id      int    `json:"id" example:"2"`
	Name    string `json:"name" example:"central-panel-a"`
	Token   string `json:"token,omitempty" example:"new-token-string"`
	Enabled bool   `json:"enabled" example:"true"`
	// Namespaces are the tag/email prefixes this token may write. Empty means
	// unrestricted; a non-empty list means every object the token creates,
	// edits or deletes must carry one of these prefixes.
	Namespaces []string `json:"namespaces"`
	CreatedAt  int64    `json:"createdAt" example:"1736000000"`
}

func apiTokenCreatedAtSeconds(createdAt int64) int64 {
	if createdAt >= model.ApiTokenUnixMillisecondsThreshold {
		return createdAt / 1000
	}
	return createdAt
}

// toView builds the metadata view returned by List. It never carries the
// token value: only a SHA-256 hash is stored, and the plaintext is shown
// exactly once at creation time.
func toView(t *model.ApiToken) *ApiTokenView {
	return &ApiTokenView{
		Id:         t.Id,
		Name:       t.Name,
		Enabled:    t.Enabled,
		Namespaces: service.ParseNamespaces(t.Namespaces),
		CreatedAt:  apiTokenCreatedAtSeconds(t.CreatedAt),
	}
}

func (s *ApiTokenService) List() ([]*ApiTokenView, error) {
	db := database.GetDB()
	var rows []*model.ApiToken
	if err := db.Model(model.ApiToken{}).Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*ApiTokenView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	return out, nil
}

func (s *ApiTokenService) Create(name string, namespaces []string) (*ApiTokenView, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, common.NewError("token name is required")
	}
	if len(name) > 64 {
		return nil, common.NewError("token name must be 64 characters or fewer")
	}
	db := database.GetDB()
	var count int64
	if err := db.Model(model.ApiToken{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, common.NewError("a token with that name already exists")
	}
	stored, err := service.JoinNamespaces(namespaces)
	if err != nil {
		return nil, err
	}
	plaintext := random.Seq(apiTokenLength)
	row := &model.ApiToken{
		Name:       name,
		Token:      crypto.HashTokenSHA256(plaintext),
		Enabled:    true,
		Namespaces: stored,
	}
	if err := db.Create(row).Error; err != nil {
		return nil, err
	}
	view := toView(row)
	view.Token = plaintext
	return view, nil
}

func (s *ApiTokenService) Delete(id int) error {
	if id <= 0 {
		return common.NewError("invalid token id")
	}
	db := database.GetDB()
	return db.Where("id = ?", id).Delete(model.ApiToken{}).Error
}

func (s *ApiTokenService) SetEnabled(id int, enabled bool) error {
	if id <= 0 {
		return common.NewError("invalid token id")
	}
	db := database.GetDB()
	res := db.Model(model.ApiToken{}).Where("id = ?", id).Update("enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("token not found")
	}
	return nil
}

// Match returns true when the presented bearer token matches any enabled
// row in api_tokens.
func (s *ApiTokenService) Match(presented string) bool {
	return s.MatchToken(presented) != nil
}

// MatchToken returns the enabled row the presented bearer token belongs to, or
// nil. Tokens are stored as SHA-256 hashes, so the presented value is hashed
// before a constant-time compare per row keeps a remote attacker from timing
// the comparison byte-by-byte; every row is compared even after a hit so the
// time taken does not depend on which row matched.
func (s *ApiTokenService) MatchToken(presented string) *model.ApiToken {
	if presented == "" {
		return nil
	}
	db := database.GetDB()
	var rows []*model.ApiToken
	if err := db.Model(model.ApiToken{}).Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return nil
	}
	presentedHash := []byte(crypto.HashTokenSHA256(presented))
	var matched *model.ApiToken
	for _, r := range rows {
		if subtle.ConstantTimeCompare([]byte(r.Token), presentedHash) == 1 {
			matched = r
		}
	}
	return matched
}

// SetNamespaces replaces the prefixes a token owns.
func (s *ApiTokenService) SetNamespaces(id int, namespaces []string) error {
	if id <= 0 {
		return common.NewError("invalid token id")
	}
	stored, err := service.JoinNamespaces(namespaces)
	if err != nil {
		return err
	}
	db := database.GetDB()
	res := db.Model(model.ApiToken{}).Where("id = ?", id).Update("namespaces", stored)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("token not found")
	}
	return nil
}
