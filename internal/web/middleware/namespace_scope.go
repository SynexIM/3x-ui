package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	NamespaceScopeContextKey = "api_token_namespaces"
	NodeSettingsContextKey   = "api_token_node_settings"
)

var identityKeys = map[string]bool{"tag": true, "ruleTag": true, "email": true, "egress_tag": true}

const maxScopeInspectionBytes = 8 << 20

func NamespaceScopeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		prefixes, _ := c.Get(NamespaceScopeContextKey)
		owned, _ := prefixes.([]string)
		if len(owned) == 0 || !isMutation(c.Request.Method) {
			c.Next()
			return
		}

		resolver, registered := ScopeRouteResolver(c.Request.Method, c.FullPath())
		if !registered {
			refuseScope(c, "this token owns "+strings.Join(owned, ", ")+" and may not touch route "+c.FullPath())
			return
		}
		identities, err := resolver(c)
		if err != nil {
			refuseScope(c, "this token owns "+strings.Join(owned, ", ")+": "+err.Error())
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

func collectIdentities(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		// Routing users identify clients; outbound users identify remote accounts.
		if isRoutingRule(typed) {
			collectStrings(typed["user"], out)
		}
		for key, nested := range typed {
			if key == "emails" || key == "tags" {
				collectStrings(nested, out)
				continue
			}
			if key == "settings" {
				if encoded, ok := nested.(string); ok {
					var decoded any
					if json.Unmarshal([]byte(encoded), &decoded) == nil {
						collectIdentities(decoded, out)
					}
					continue
				}
			}
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
