// Package grpc 实现 mdm.customer.v1.CustomerService——内部 gRPC 面（§2.1）。
// HTTP 与 gRPC 共用同一个 service.Service，业务逻辑只写一遍。
//
// ⚠️ 与 backend/internal/http 同样的已知范围缩减：Customer 消息里的
// contacts/billing_infos 数组目前总是空的（AddContact 写得进去，但读
// 路径还没有联查），留到后续任务补聚合查询。
package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/timestamppb"

	customerv1 "github.com/brickKit/mdm-customer/gen/mdm/customer/v1"

	"github.com/brickKit/mdm-customer/backend/internal/repo"
	"github.com/brickKit/mdm-customer/backend/internal/service"
)

type server struct {
	customerv1.UnimplementedCustomerServiceServer
	svc *service.Service
}

// New 构造 gRPC 服务端实现。module.go 用它注册到 grpc.Server。
func New(svc *service.Service) customerv1.CustomerServiceServer {
	return &server{svc: svc}
}

func toProtoStatus(s string) customerv1.CustomerStatus {
	if s == "DISABLED" {
		return customerv1.CustomerStatus_CUSTOMER_STATUS_DISABLED
	}
	return customerv1.CustomerStatus_CUSTOMER_STATUS_ACTIVE
}

func fromProtoStatus(s customerv1.CustomerStatus) string {
	if s == customerv1.CustomerStatus_CUSTOMER_STATUS_DISABLED {
		return "DISABLED"
	}
	return "ACTIVE"
}

func toProtoCustomer(c *repo.Customer) *customerv1.Customer {
	return &customerv1.Customer{
		Id: c.ID, Code: c.Code, Name: c.Name, TaxNo: c.TaxNo,
		CreditLimit: c.CreditLimit, Status: toProtoStatus(c.Status), Version: c.Version,
		CreatedAt: timestamppb.New(c.CreatedAt), UpdatedAt: timestamppb.New(c.UpdatedAt),
	}
}

func (s *server) Create(ctx context.Context, req *customerv1.CreateRequest) (*customerv1.CreateResponse, error) {
	c, err := s.svc.Create(ctx, repo.CreateInput{
		IdempotencyKey: req.IdempotencyKey, Code: req.Code, Name: req.Name,
		TaxNo: req.TaxNo, CreditLimit: req.CreditLimit,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &customerv1.CreateResponse{Customer: toProtoCustomer(c)}, nil
}

func (s *server) Update(ctx context.Context, req *customerv1.UpdateRequest) (*customerv1.UpdateResponse, error) {
	c, err := s.svc.Update(ctx, service.UpdateInput{
		IdempotencyKey: req.IdempotencyKey, ID: req.Id, Version: req.Version,
		Name: req.Name, TaxNo: req.TaxNo, CreditLimit: req.CreditLimit,
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &customerv1.UpdateResponse{Customer: toProtoCustomer(c)}, nil
}

func (s *server) SetStatus(ctx context.Context, req *customerv1.SetStatusRequest) (*customerv1.SetStatusResponse, error) {
	c, err := s.svc.SetStatus(ctx, service.SetStatusInput{
		IdempotencyKey: req.IdempotencyKey, ID: req.Id, Version: req.Version,
		Status: fromProtoStatus(req.Status),
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &customerv1.SetStatusResponse{Customer: toProtoCustomer(c)}, nil
}

func (s *server) AddContact(ctx context.Context, req *customerv1.AddContactRequest) (*customerv1.AddContactResponse, error) {
	contact := req.GetContact()
	out, err := s.svc.AddContact(ctx, repo.AddContactInput{
		IdempotencyKey: req.IdempotencyKey, CustomerID: req.CustomerId,
		Name: contact.GetName(), Phone: contact.GetPhone(),
		Email: contact.GetEmail(), Primary: contact.GetPrimary(),
	})
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return &customerv1.AddContactResponse{Contact: &customerv1.Contact{
		Id: out.ID, Name: out.Name, Phone: out.Phone, Email: out.Email, Primary: out.Primary,
	}}, nil
}

func (s *server) Get(ctx context.Context, req *customerv1.GetRequest) (*customerv1.Customer, error) {
	c, err := s.svc.Get(ctx, req.Id)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	return toProtoCustomer(c), nil
}

func (s *server) List(ctx context.Context, req *customerv1.ListRequest) (*customerv1.ListResponse, error) {
	in := repo.ListInput{Cursor: req.Cursor, PageSize: int(req.PageSize)}
	if req.StatusFilter != customerv1.CustomerStatus_CUSTOMER_STATUS_UNSPECIFIED {
		in.StatusFilter = fromProtoStatus(req.StatusFilter)
	}
	if req.CreatedAfter != nil {
		in.CreatedAfter = req.CreatedAfter.AsTime()
	}
	if req.CreatedBefore != nil {
		in.CreatedBefore = req.CreatedBefore.AsTime()
	}
	out, err := s.svc.List(ctx, in)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	customers := make([]*customerv1.Customer, 0, len(out.Customers))
	for _, c := range out.Customers {
		customers = append(customers, toProtoCustomer(c))
	}
	return &customerv1.ListResponse{Customers: customers, NextCursor: out.NextCursor}, nil
}

// BatchGet 是 BFF 层 GraphQL Resolver 防 N+1 的唯一合法调用方式（§3.8）。
func (s *server) BatchGet(ctx context.Context, req *customerv1.BatchGetRequest) (*customerv1.BatchGetResponse, error) {
	found, missing, err := s.svc.BatchGet(ctx, req.Ids)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	customers := make([]*customerv1.Customer, 0, len(found))
	for _, c := range found {
		customers = append(customers, toProtoCustomer(c))
	}
	return &customerv1.BatchGetResponse{Customers: customers, MissingIds: missing}, nil
}

func (s *server) GetSummary(ctx context.Context, req *customerv1.GetSummaryRequest) (*customerv1.GetSummaryResponse, error) {
	found, _, err := s.svc.BatchGet(ctx, req.Ids)
	if err != nil {
		return nil, service.ToStatus(err)
	}
	summaries := make([]*customerv1.CustomerSummary, 0, len(found))
	for _, c := range found {
		summaries = append(summaries, &customerv1.CustomerSummary{
			Id: c.ID, Code: c.Code, Name: c.Name, Status: toProtoStatus(c.Status), Version: c.Version,
		})
	}
	return &customerv1.GetSummaryResponse{Summaries: summaries}, nil
}
