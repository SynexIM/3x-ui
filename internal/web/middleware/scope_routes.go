package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type scopeResolver func(*gin.Context) ([]string, error)

func scopeRouteKey(method, path string) string {
	_, relative, ok := strings.Cut(path, "/panel/api/")
	if ok {
		path = "/panel/api/" + relative
	}
	return method + " " + strings.ReplaceAll(path, `\:`, ":")
}

func ScopeRouteResolver(method, path string) (scopeResolver, bool) {
	resolver, ok := scopeRoutes[scopeRouteKey(method, path)]
	return resolver, ok
}

func ScopeRoutePolicy(method, path string) (bool, string) {
	key := scopeRouteKey(method, path)
	_, registered := scopeRoutes[key]
	return registered, ScopeUnavailableRoutes[key]
}

func scopeBody(c *gin.Context, target any) error {
	if c.Request.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxScopeInspectionBytes+1))
	_ = c.Request.Body.Close()
	if err != nil {
		return err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > maxScopeInspectionBytes {
		return fmt.Errorf("request body exceeds scope inspection limit")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if !strings.HasPrefix(c.GetHeader("Content-Type"), "application/json") {
		return fmt.Errorf("scope resolver requires application/json")
	}
	return json.Unmarshal(body, target)
}

func scopeRead(*gin.Context) ([]string, error) { return nil, nil }

func scopeNames(c *gin.Context) ([]string, error) {
	var body any
	if err := scopeBody(c, &body); err != nil {
		return nil, err
	}
	names := []string{}
	for _, key := range []string{"tag", "email"} {
		if value := c.Param(key); value != "" {
			names = append(names, value)
		}
	}
	collectIdentities(body, &names)
	if len(names) == 0 {
		return nil, fmt.Errorf("request names no object")
	}
	return names, nil
}

func inboundScopeNames(ids []int) ([]string, error) {
	names := []string{}
	for _, id := range ids {
		var row model.Inbound
		if id <= 0 {
			return nil, fmt.Errorf("inbound %d does not exist", id)
		}
		if err := database.GetDB().First(&row, id).Error; err != nil {
			return nil, fmt.Errorf("inbound %d does not exist: %w", id, err)
		}
		if row.Tag == "" {
			return nil, fmt.Errorf("inbound %d has no tag", id)
		}
		names = append(names, row.Tag)
	}
	return names, nil
}

func scopeInbound(c *gin.Context) ([]string, error) {
	var body struct {
		Ids       []int `json:"ids"`
		Fallbacks []struct {
			ChildID int `json:"childId"`
		} `json:"fallbacks"`
	}
	if err := scopeBody(c, &body); err != nil {
		return nil, err
	}
	if id := c.Param("id"); id != "" {
		parsed, err := strconv.Atoi(id)
		if err != nil {
			return nil, fmt.Errorf("invalid inbound id %q", id)
		}
		body.Ids = append(body.Ids, parsed)
	}
	for _, fallback := range body.Fallbacks {
		if fallback.ChildID > 0 {
			body.Ids = append(body.Ids, fallback.ChildID)
		}
	}
	if len(body.Ids) == 0 {
		return nil, fmt.Errorf("request names no inbound")
	}
	names, err := inboundScopeNames(body.Ids)
	if err != nil {
		return nil, err
	}
	// Updates must own both the stored tag and any new identities in the body.
	var decoded any
	if err := scopeBody(c, &decoded); err != nil {
		return nil, err
	}
	collectIdentities(decoded, &names)
	return names, nil
}

func scopeClient(c *gin.Context) ([]string, error) {
	names, err := scopeNames(c)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := scopeBody(c, &decoded); err != nil {
		return nil, err
	}
	var ids []int
	var visit func(any) error
	visit = func(value any) error {
		switch object := value.(type) {
		case map[string]any:
			for key, nested := range object {
				if key == "inboundIds" {
					raw, _ := json.Marshal(nested)
					var parsed []int
					if err := json.Unmarshal(raw, &parsed); err != nil {
						return err
					}
					ids = append(ids, parsed...)
				} else if err := visit(nested); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range object {
				if err := visit(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(decoded); err != nil {
		return nil, err
	}
	inboundNames, err := inboundScopeNames(ids)
	return append(names, inboundNames...), err
}

func scopeHost(c *gin.Context) ([]string, error) {
	var body struct {
		InboundIds []int    `json:"inboundIds"`
		Ids        []string `json:"ids"`
		GroupId    string   `json:"groupId"`
	}
	if err := scopeBody(c, &body); err != nil {
		return nil, err
	}
	if id := c.Param("groupId"); id != "" {
		body.Ids = append(body.Ids, id)
	}
	if strings.HasSuffix(c.FullPath(), "/add") && body.GroupId != "" {
		body.Ids = append(body.Ids, body.GroupId)
	}
	for _, id := range body.Ids {
		var rows []model.Host
		if err := database.GetDB().Where("group_id = ?", id).Find(&rows).Error; err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			if strings.HasSuffix(c.FullPath(), "/add") && id == body.GroupId {
				continue
			}
			return nil, fmt.Errorf("host %q does not exist", id)
		}
		for _, row := range rows {
			body.InboundIds = append(body.InboundIds, row.InboundId)
		}
	}
	if len(body.InboundIds) == 0 {
		return nil, fmt.Errorf("request names no host inbound")
	}
	return inboundScopeNames(body.InboundIds)
}

func scopeClasses(c *gin.Context) ([]string, error) {
	var body struct {
		Classes []struct {
			Name string `json:"name"`
		} `json:"classes"`
	}
	if err := scopeBody(c, &body); err != nil {
		return nil, err
	}
	names := []string{}
	for _, class := range body.Classes {
		names = append(names, class.Name)
	}
	return names, nil
}

var scopeRoutes = map[string]scopeResolver{
	"DELETE /panel/api/clients/hwids/:email":       scopeClient,
	"DELETE /panel/api/clients/hwids/:email/:id":   scopeClient,
	"DELETE /panel/api/outbounds/:tag":             scopeNames,
	"DELETE /panel/api/routing/rules/:tag":         scopeNames,
	"PATCH /panel/api/outbounds/:tag":              scopeNames,
	"POST /panel/api/clients/:email/attach":        scopeClient,
	"POST /panel/api/clients/:email/credentials":   scopeClient,
	"POST /panel/api/clients/:email/detach":        scopeClient,
	"POST /panel/api/clients/:email/externalLinks": scopeClient,
	"POST /panel/api/clients/activeInbounds":       scopeRead,
	"POST /panel/api/clients/add":                  scopeClient,
	"POST /panel/api/clients/bulkAdjust":           scopeClient,
	"POST /panel/api/clients/bulkAttach":           scopeClient,
	"POST /panel/api/clients/bulkCreate":           scopeClient,
	"POST /panel/api/clients/bulkDel":              scopeClient,
	"POST /panel/api/clients/bulkDetach":           scopeClient,
	"POST /panel/api/clients/bulkDisable":          scopeClient,
	"POST /panel/api/clients/bulkEnable":           scopeClient,
	"POST /panel/api/clients/bulkResetTraffic":     scopeClient,
	"POST /panel/api/clients/clearIps/:email":      scopeClient,
	"POST /panel/api/clients/clientIpsByGuid":      scopeRead,
	"POST /panel/api/clients/del/:email":           scopeClient,
	"POST /panel/api/clients/happLink/:id":         scopeRead,
	"POST /panel/api/clients/hwids/:email":         scopeRead,
	"POST /panel/api/clients/ips/:email":           scopeRead,
	"POST /panel/api/clients/lastOnline":           scopeRead,
	"POST /panel/api/clients/onlines":              scopeRead,
	"POST /panel/api/clients/onlinesByGuid":        scopeRead,
	"POST /panel/api/clients/resetTraffic/:email":  scopeClient,
	"POST /panel/api/clients/runtime/:email":       scopeClient,
	"POST /panel/api/clients/update/:email":        scopeClient,
	"POST /panel/api/clients/updateTraffic/:email": scopeClient,
	"POST /panel/api/hosts/add":                    scopeHost,
	"POST /panel/api/hosts/bulk/add":               scopeHost,
	"POST /panel/api/hosts/bulk/del":               scopeHost,
	"POST /panel/api/hosts/bulk/setEnable":         scopeHost,
	"POST /panel/api/hosts/del/:groupId":           scopeHost,
	"POST /panel/api/hosts/reorder":                scopeHost,
	"POST /panel/api/hosts/setEnable/:groupId":     scopeHost,
	"POST /panel/api/hosts/update/:groupId":        scopeHost,
	"POST /panel/api/inbounds/:id/delAllClients":   scopeInbound,
	"POST /panel/api/inbounds/:id/fallbacks":       scopeInbound,
	"POST /panel/api/inbounds/:id/resetTraffic":    scopeInbound,
	"POST /panel/api/inbounds/:id/subSortIndex":    scopeInbound,
	"POST /panel/api/inbounds/add":                 scopeNames,
	"POST /panel/api/inbounds/bulkDel":             scopeInbound,
	"POST /panel/api/inbounds/del/:id":             scopeInbound,
	"POST /panel/api/inbounds/setEnable/:id":       scopeInbound,
	"POST /panel/api/inbounds/update/:id":          scopeInbound,
	"POST /panel/api/inbounds/validate":            scopeRead,
	"POST /panel/api/nodes/certFingerprint":        scopeRead,
	"POST /panel/api/nodes/fairshare":              scopeClasses,
	"POST /panel/api/nodes/inbounds":               scopeRead,
	"POST /panel/api/nodes/ingress-probe":          scopeRead,
	"POST /panel/api/nodes/test":                   scopeRead,
	"POST /panel/api/outbounds":                    scopeNames,
	"POST /panel/api/routing/rules":                scopeNames,
	"POST /panel/api/routing/rules:batch":          scopeNames,
	"POST /panel/api/server/amneziawglogs/:count":  scopeRead,
	"POST /panel/api/server/getCertHash":           scopeRead,
	"POST /panel/api/server/getNewEchCert":         scopeRead,
	"POST /panel/api/server/getRemoteCertHash":     scopeRead,
	"POST /panel/api/server/logs/:count":           scopeRead,
	"POST /panel/api/server/scanRealityTarget":     scopeRead,
	"POST /panel/api/server/scanRealityTargets":    scopeRead,
	"POST /panel/api/server/xraylogs/:count":       scopeRead,
	"POST /panel/api/setting/all":                  scopeRead,
	"POST /panel/api/setting/defaultSettings":      scopeRead,
	"POST /panel/api/setting/factoryDefaults":      scopeRead,
	"POST /panel/api/setting/validateRegex":        scopeRead,
	"POST /panel/api/xray/":                        scopeRead,
	"POST /panel/api/xray/balancerStatus":          scopeRead,
	"POST /panel/api/xray/geodata/validate":        scopeRead,
	"POST /panel/api/xray/outbound-subs/parse":     scopeRead,
	"POST /panel/api/xray/routeTest":               scopeRead,
	"POST /panel/api/xray/testOutbound":            scopeRead,
	"POST /panel/api/xray/testOutbounds":           scopeRead,
}

// ScopeUnavailableRoutes records why a mutation cannot use a namespaced token.
var ScopeUnavailableRoutes = map[string]string{
	"DELETE /panel/api/sub-balancers/:id":                 "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"DELETE /panel/api/xray/outbound-subs/:id":            "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /getTwoFactorEnable":                            "登录会话接口，不提供作用域令牌认证",
	"POST /login":                                         "登录会话接口，不提供作用域令牌认证",
	"POST /logout":                                        "登录会话接口，不提供作用域令牌认证",
	"POST /panel/api/backuptotgbot":                       "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/clients/delDepleted":                 "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/clients/delOrphans":                  "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/clients/groups/bulkAdd":              "客户端分组批量操作，组名不代表全部成员身份",
	"POST /panel/api/clients/groups/bulkRemove":           "客户端分组批量操作，组名不代表全部成员身份",
	"POST /panel/api/clients/groups/create":               "客户端分组批量操作，组名不代表全部成员身份",
	"POST /panel/api/clients/groups/delete":               "客户端分组批量操作，组名不代表全部成员身份",
	"POST /panel/api/clients/groups/rename":               "客户端分组批量操作，组名不代表全部成员身份",
	"POST /panel/api/clients/groups/resetTraffic":         "客户端分组批量操作，组名不代表全部成员身份",
	"POST /panel/api/clients/import":                      "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/clients/resetAllTraffics":            "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/declarative/abort":                   "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/declarative/apply-delta":             "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/declarative/commit":                  "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/declarative/stage":                   "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/inbounds/import":                     "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/inbounds/pushClientTraffics":         "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/inbounds/resetAllTraffics":           "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/nodes/add":                           "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/del/:id":                       "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/mtls/ca":                       "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/mtls/reloadClient":             "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/mtls/trustCA":                  "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/probe/:id":                     "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/setEnable/:id":                 "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/update/:id":                    "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/nodes/updatePanel":                   "节点管理或证书变更，无命名空间身份",
	"POST /panel/api/server/clientIps":                    "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/importDB":                     "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/installXray/:version":         "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/restartXrayService":           "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/setUpdateChannel":             "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/stopXrayService":              "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/updateGeofile":                "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/updateGeofile/:fileName":      "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/server/updatePanel":                  "全局进程、软件、数据库或遥测写入，无命名空间身份",
	"POST /panel/api/setting/apiTokens/create":            "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/apiTokens/delete/:id":        "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/apiTokens/setEnabled/:id":    "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/apiTokens/setNamespaces/:id": "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/restartPanel":                "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/testDiscord":                 "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/testSmtp":                    "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/testTgBot":                   "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/update":                      "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/setting/updateUser":                  "面板设置、凭据或令牌管理，属于全局管理权限",
	"POST /panel/api/sub-balancers":                       "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/sub-balancers/:id":                   "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/sub-balancers/:id/del":               "全局批处理、导入或订阅配置，不能按请求对象前缀隔离",
	"POST /panel/api/xray/balancerOverride":               "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/nord/:action":                   "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/outbound-subs":                  "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/outbound-subs/:id":              "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/outbound-subs/:id/del":          "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/outbound-subs/:id/move":         "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/outbound-subs/:id/refresh":      "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/pia/:action":                    "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/resetOutboundsTraffic":          "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/update":                         "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
	"POST /panel/api/xray/warp/:action":                   "整机配置、订阅源或运行态变更，不能按对象前缀隔离",
}
