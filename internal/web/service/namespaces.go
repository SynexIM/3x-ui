package service

import (
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// Managed namespaces: how a person and an automation share one panel.
//
// An API token declares the tag/email prefixes it owns. Objects carrying one of
// those prefixes belong to that automation; everything else belongs to whoever
// is sitting at the panel. Neither side is locked out of the other's objects —
// a local edit of a managed object goes through, and the page says a later
// reconciliation may put the automated value back — but the automation itself
// is confined: it cannot create, edit or delete anything outside its prefixes.
//
// The prefixes are not baked in. Whatever you point at this panel declares its
// own, so the mechanism is worth having whether the automation is a fleet
// controller, a billing system or a shell script.

// ParseNamespaces splits a stored comma-separated prefix list. Whitespace and
// empty entries are dropped, so a list typed with spaces means what it looks like.
func ParseNamespaces(stored string) []string {
	out := []string{}
	for _, part := range strings.Split(stored, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// JoinNamespaces normalizes a prefix list for storage, refusing shapes that
// would silently own more than the operator meant.
func JoinNamespaces(prefixes []string) (string, error) {
	cleaned := make([]string, 0, len(prefixes))
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		trimmed := strings.TrimSpace(prefix)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, ",") {
			return "", common.NewErrorf("namespace prefix %q may not contain a comma", trimmed)
		}
		if len(trimmed) < 2 {
			return "", common.NewErrorf("namespace prefix %q is too short to be a namespace; use at least two characters", trimmed)
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		cleaned = append(cleaned, trimmed)
	}
	return strings.Join(cleaned, ","), nil
}

// ManagedNamespaces is the union of every enabled token's prefixes: the set of
// namespaces on this panel that some automation owns. The pages read it to mark
// which objects a reconciliation may overwrite.
func ManagedNamespaces() []string {
	db := database.GetDB()
	if db == nil {
		return []string{}
	}
	var rows []*model.ApiToken
	if err := db.Model(model.ApiToken{}).Where("enabled = ?", true).Find(&rows).Error; err != nil {
		return []string{}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, row := range rows {
		for _, prefix := range ParseNamespaces(row.Namespaces) {
			if !seen[prefix] {
				seen[prefix] = true
				out = append(out, prefix)
			}
		}
	}
	sort.Strings(out)
	return out
}

// IsManagedName reports whether an object's tag or email falls inside a
// namespace some automation owns.
func IsManagedName(name string, namespaces []string) bool {
	for _, prefix := range namespaces {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
