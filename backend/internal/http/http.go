// Package http 是 mdm-customer 的 REST 面（对外路径前缀 /mdm/customer，
// 与 assembly.yaml 的 edge_routes 一致）。/healthz、/metrics 已经由
// besdk.NewGinEngine 统一挂好（零依赖、恒 200，§12.3.6），这里不重复挂、
// 也不写进 contracts/customer.openapi.yaml（那条契约按 servers:
// /mdm/customer 为前缀，写进去会隐含错误的路径）。
//
// ⚠️ 已知的范围缩减：这里返回的 Customer 不含 contacts/billing_infos
// 数组（AddContact 写得进去，但 Get/List/BatchGet 目前不会把它们
// 联查出来）——这是有意义的简化，不是漏了没做，留到后续任务再补上
// 聚合查询。
package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	besdk "github.com/brickKit/be-sdk-go"
	"github.com/brickKit/mdm-customer/backend/internal/repo"
	"github.com/brickKit/mdm-customer/backend/internal/service"
)

// RegisterRoutes 挂载业务路由。eng 已经是 besdk.NewGinEngine 产出的、
// 挂好中间件的 engine——这里只负责注册业务 handler。
//
// 阶段三 Task 6：权限键从阶段二的 besdk.Public 换成 assembly.yaml 里
// 声明的真实键——判定本体已在 Task 5 换成真实 bundle 查找，这一步只改
// 调用点的字面量。`data_scopes: none`（客户主数据全员可见），本组件不
// 需要任何数据范围过滤。
func RegisterRoutes(eng *gin.Engine, svc *service.Service) {
	g := eng.Group("/mdm/customer")
	besdk.GET(g, "/customers", "mdm.customer.view", listHandler(svc))
	besdk.GET(g, "/customers/:id", "mdm.customer.view", getHandler(svc))
	besdk.POST(g, "/customers", "mdm.customer.create", createHandler(svc))
	besdk.PATCH(g, "/customers/:id", "mdm.customer.update", updateHandler(svc))
	besdk.POST(g, "/customers/:id/status", "mdm.customer.set_status", setStatusHandler(svc))
	besdk.POST(g, "/customers/:id/contacts", "mdm.customer.add_contact", addContactHandler(svc))
}

type customerDTO struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	TaxNo       string `json:"tax_no"`
	CreditLimit string `json:"credit_limit"`
	Status      string `json:"status"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toDTO(c *repo.Customer) customerDTO {
	return customerDTO{
		ID: c.ID, Code: c.Code, Name: c.Name, TaxNo: c.TaxNo,
		CreditLimit: c.CreditLimit, Status: c.Status, Version: c.Version,
		CreatedAt: c.CreatedAt.Format(rfc3339), UpdatedAt: c.UpdatedAt.Format(rfc3339),
	}
}

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

type createRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Code           string `json:"code"`
	Name           string `json:"name" binding:"required"`
	TaxNo          string `json:"tax_no"`
	CreditLimit    string `json:"credit_limit"`
}

func createHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.Create(c.Request.Context(), repo.CreateInput{
			IdempotencyKey: req.IdempotencyKey, Code: req.Code, Name: req.Name,
			TaxNo: req.TaxNo, CreditLimit: req.CreditLimit,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

func getHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		out, err := svc.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

func listHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		pageSize, _ := strconv.Atoi(c.Query("page_size"))
		out, err := svc.List(c.Request.Context(), repo.ListInput{
			Cursor:       c.Query("cursor"),
			PageSize:     pageSize,
			StatusFilter: c.Query("status_filter"),
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		dtos := make([]customerDTO, 0, len(out.Customers))
		for _, cust := range out.Customers {
			dtos = append(dtos, toDTO(cust))
		}
		c.JSON(http.StatusOK, gin.H{"customers": dtos, "next_cursor": out.NextCursor})
	}
}

type updateRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Version        int64  `json:"version" binding:"required"`
	Name           string `json:"name"`
	TaxNo          string `json:"tax_no"`
	CreditLimit    string `json:"credit_limit"`
}

func updateHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req updateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.Update(c.Request.Context(), service.UpdateInput{
			IdempotencyKey: req.IdempotencyKey, ID: c.Param("id"), Version: req.Version,
			Name: req.Name, TaxNo: req.TaxNo, CreditLimit: req.CreditLimit,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

type setStatusRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Version        int64  `json:"version" binding:"required"`
	Status         string `json:"status" binding:"required"`
}

func setStatusHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req setStatusRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.SetStatus(c.Request.Context(), service.SetStatusInput{
			IdempotencyKey: req.IdempotencyKey, ID: c.Param("id"),
			Version: req.Version, Status: req.Status,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, toDTO(out))
	}
}

type addContactRequest struct {
	IdempotencyKey string `json:"idempotency_key" binding:"required"`
	Contact        struct {
		Name    string `json:"name" binding:"required"`
		Phone   string `json:"phone"`
		Email   string `json:"email"`
		Primary bool   `json:"primary"`
	} `json:"contact" binding:"required"`
}

func addContactHandler(svc *service.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req addContactRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		out, err := svc.AddContact(c.Request.Context(), repo.AddContactInput{
			IdempotencyKey: req.IdempotencyKey, CustomerID: c.Param("id"),
			Name: req.Contact.Name, Phone: req.Contact.Phone,
			Email: req.Contact.Email, Primary: req.Contact.Primary,
		})
		if err != nil {
			_ = c.Error(service.ToStatus(err))
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id": out.ID, "name": out.Name, "phone": out.Phone,
			"email": out.Email, "primary": out.Primary,
		})
	}
}
