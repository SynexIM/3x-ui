package controller

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

// XrayObjectController exposes outbounds and routing rules one object at a
// time, applied to the running core over its gRPC API.
//
// Before these existed the only way to add an outbound was to resend the whole
// template, which meant reproducing every other object byte for byte and,
// whenever the diff touched something with no reload API, dropping every
// connection on the node.
type XrayObjectController struct {
	objectService service.XrayObjectService
}

// NewXrayObjectController registers the outbound and routing-rule routes on
// the API group (paths become /panel/api/outbounds and /panel/api/routing/*).
func NewXrayObjectController(g *gin.RouterGroup) *XrayObjectController {
	a := &XrayObjectController{}
	a.initRouter(g)
	return a
}

func (a *XrayObjectController) initRouter(g *gin.RouterGroup) {
	g.GET("/outbounds", a.listOutbounds)
	g.POST("/outbounds", a.addOutbound)
	g.PATCH("/outbounds/:tag", a.updateOutbound)
	g.DELETE("/outbounds/:tag", a.deleteOutbound)

	// Read-only: what the node is actually running right now, straight from the
	// core. Nothing on this path writes anything.
	g.GET("/runtime", a.runtimeSnapshot)

	g.GET("/routing/rules", a.listRoutingRules)
	g.POST("/routing/rules", a.addRoutingRule)
	// The escaped colon keeps gin from reading ":batch" as a path parameter;
	// the route still matches the literal /routing/rules:batch.
	g.POST("/routing/rules\\:batch", a.addRoutingRulesBatch)
	g.DELETE("/routing/rules/:tag", a.deleteRoutingRule)
}

func (a *XrayObjectController) listOutbounds(c *gin.Context) {
	view, err := a.objectService.ListOutbounds()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.getSettings"), err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *XrayObjectController) addOutbound(c *gin.Context) {
	body, err := readJSONObject(c)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	result, err := a.objectService.AddOutbound(body)
	a.answer(c, result, err)
}

func (a *XrayObjectController) updateOutbound(c *gin.Context) {
	body, err := readJSONObject(c)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	result, err := a.objectService.UpdateOutbound(c.Param("tag"), body)
	a.answer(c, result, err)
}

func (a *XrayObjectController) deleteOutbound(c *gin.Context) {
	result, err := a.objectService.DeleteOutbound(c.Param("tag"))
	a.answer(c, result, err)
}

func (a *XrayObjectController) runtimeSnapshot(c *gin.Context) {
	view, err := a.objectService.RuntimeSnapshot()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.getSettings"), err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *XrayObjectController) listRoutingRules(c *gin.Context) {
	view, err := a.objectService.ListRoutingRules()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.getSettings"), err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *XrayObjectController) addRoutingRule(c *gin.Context) {
	body, err := readJSONObject(c)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	result, err := a.objectService.AddRoutingRules([]json.RawMessage{body})
	a.answer(c, result, err)
}

// addRoutingRulesBatch takes either a bare JSON array of rules or an object
// with a "rules" array, because both shapes are what callers actually send.
func (a *XrayObjectController) addRoutingRulesBatch(c *gin.Context) {
	body, err := readJSONObject(c)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	var rules []json.RawMessage
	if unmarshalErr := json.Unmarshal(body, &rules); unmarshalErr != nil {
		var wrapper struct {
			Rules []json.RawMessage `json:"rules"`
		}
		if json.Unmarshal(body, &wrapper) != nil || wrapper.Rules == nil {
			jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"),
				errors.New("expected a JSON array of routing rules, or an object with a \"rules\" array"))
			return
		}
		rules = wrapper.Rules
	}
	result, err := a.objectService.AddRoutingRules(rules)
	a.answer(c, result, err)
}

func (a *XrayObjectController) deleteRoutingRule(c *gin.Context) {
	result, err := a.objectService.DeleteRoutingRule(c.Param("tag"))
	a.answer(c, result, err)
}

// answer maps an unknown tag to 404 so a caller can tell "never existed" from
// "the core refused it", which the shared error envelope alone cannot express.
func (a *XrayObjectController) answer(c *gin.Context, result *service.ObjectApplyResult, err error) {
	if errors.Is(err, service.ErrXrayObjectNotFound) {
		pureJsonMsg(c, http.StatusNotFound, false, err.Error())
		return
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.settings.toasts.modifySettings"), err)
		return
	}
	jsonObj(c, result, nil)
}

// readJSONObject reads the request body and refuses anything that is not JSON
// before the service has to guess what a malformed object meant.
func readJSONObject(c *gin.Context) (json.RawMessage, error) {
	body, err := c.GetRawData()
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, errors.New("the request body is not valid JSON")
	}
	return json.RawMessage(body), nil
}
