package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func (a *NodeController) ingressProbe(c *gin.Context) {
	var input service.IngressProbeRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "invalid ingress probe", err)
		return
	}
	jsonObj(c, service.ProbeIngress(c.Request.Context(), input), nil)
}
