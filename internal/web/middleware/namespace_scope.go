package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// NamespaceScopeContextKey carries the prefixes the authenticated API token
// owns. Absent or empty means the caller is unrestricted, which is what a
// logged-in operator and every token created without prefixes are.
const NamespaceScopeContextKey = "api_token_namespaces"

// identityKeys are the JSON keys that name an object on this panel. A request
// that mentions any of them is talking about a specific object, and a scoped
// token may only talk about objects inside its own namespaces.
var identityKeys = map[string]bool{"tag": true, "ruleTag": true, "email": true}

// maxScopeInspectionBytes bounds how much of a body the scope check will read.
// A body larger than this is refused for a scoped token rather than waved
// through unchecked — silently skipping the check on big requests would make
// the limit the way around it.
const maxScopeInspectionBytes = 8 << 20

// NamespaceScopeMiddleware confines a scoped API token to its own namespaces.
//
// This is what lets a person and an automation share one panel without a
// read-only lock: the automation declares the prefixes it owns, and the panel
// refuses to let it create, edit or delete anything outside them. A token with
// no prefixes declared is unrestricted, so nothing that worked before changes.
//
// Reads are never scoped — seeing the whole node is what makes the panel
// useful during an incident. Writes are, and a write whose object cannot be
// identified at all is refused rather than assumed harmless: "I could not tell
// what this touches" is not a reason to allow it.
func NamespaceScopeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		prefixes, _ := c.Get(NamespaceScopeContextKey)
		owned, _ := prefixes.([]string)
		if len(owned) == 0 || !isMutation(c.Request.Method) {
			c.Next()
			return
		}

		identities, ok := requestIdentities(c)
		if !ok {
			refuseScope(c, "this token is scoped to "+strings.Join(owned, ", ")+" and the request body could not be read to check what it touches")
			return
		}
		if len(identities) == 0 {
			refuseScope(c, "this token is scoped to "+strings.Join(owned, ", ")+" and this request names no object it could own")
			return
		}
		for _, identity := range identities {
			if !hasAnyPrefix(identity, owned) {
				refuseScope(c, "this token owns "+strings.Join(owned, ", ")+" and may not touch "+identity)
				return
			}
		}
		c.Next()
	}
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func refuseScope(c *gin.Context, reason string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "msg": reason})
}

// requestIdentities collects every object name the request mentions: the :tag
// and :email path parameters, and the tag/ruleTag/email values anywhere in a
// JSON body, at any depth. Walking the whole document is deliberate — a
// whole-node declarative apply names its objects inside nested arrays, and a
// check that only looked at the top level would wave it through.
func requestIdentities(c *gin.Context) ([]string, bool) {
	identities := []string{}
	for _, name := range []string{"tag", "email"} {
		if value := strings.TrimSpace(c.Param(name)); value != "" {
			identities = append(identities, value)
		}
	}

	if !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		return identities, true
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxScopeInspectionBytes+1))
	if err != nil {
		return nil, false
	}
	_ = c.Request.Body.Close()
	if len(body) > maxScopeInspectionBytes {
		return nil, false
	}
	// The handler still has to read the body, so hand it back untouched.
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	var decoded any
	if len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &decoded) == nil {
		collectIdentities(decoded, &identities)
	}
	return identities, true
}

func collectIdentities(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		// A routing rule's "user" condition names the clients it applies to, so
		// a scoped token cannot write a rule about someone else's client. The
		// key is only read on objects that are unmistakably rules: "user" also
		// means an account name inside a socks or http outbound.
		if isRoutingRule(typed) {
			collectStrings(typed["user"], out)
		}
		for key, nested := range typed {
			if identityKeys[key] {
				if text, ok := nested.(string); ok && strings.TrimSpace(text) != "" {
					*out = append(*out, text)
					continue
				}
			}
			collectIdentities(nested, out)
		}
	case []any:
		for _, nested := range typed {
			collectIdentities(nested, out)
		}
	}
}

func isRoutingRule(object map[string]any) bool {
	if _, ok := object["ruleTag"]; ok {
		return true
	}
	kind, _ := object["type"].(string)
	return kind == "field"
}

func collectStrings(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			*out = append(*out, typed)
		}
	case []any:
		for _, nested := range typed {
			collectStrings(nested, out)
		}
	}
}
