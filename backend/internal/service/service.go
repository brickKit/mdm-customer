// Package service 是 mdm-customer 的业务规则层。判过 SOP-P：这个组件是
// 只读枢纽 + 常规 CRUD，不用任何设计模式，直接写就是最清楚的（设计计划
// §"项目结构"一节记了这个结论，见 docs/手册.md）。这一层薄——真正的
// 乐观锁判断、事件发布都在 repo 层随 SQL 一起做（同一个事务里），这里
// 只负责给 http/grpc 一个不依赖 repo 内部细节的稳定入口，外加错误日志。
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/brickKit/mdm-customer/backend/internal/repo"
)

// ErrInvalidArgument 是入参本身不合法（不是数据库层面的冲突/缺失），
// grpc/http 两层都通过 ToStatus 把它映射成 InvalidArgument/400。这一层
// 是 http/grpc 唯一共用的入口，校验只写这一处，两个协议都不用各写一遍。
var ErrInvalidArgument = errors.New("参数不合法")

type Service struct {
	repo   *repo.Repo
	logger *slog.Logger
}

func New(r *repo.Repo, logger *slog.Logger) *Service {
	return &Service{repo: r, logger: logger}
}

// validateCreditLimit 只做格式/正负号校验（合法非负小数），不代替
// NUMERIC(18,2) 的精度校验——那是数据库自己的事，多的精度校验只是把
// 数据库已经在做的事情在 Go 这层再抄一遍（决策：金额一律 string 传
// decimal，这里解析成 float64 只是临时校验用，不用于存储或运算）。
func validateCreditLimit(s string) error {
	if s == "" {
		return nil // repo 层留空时默认成 "0"
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("%w: credit_limit 不是合法数字：%q", ErrInvalidArgument, s)
	}
	if f < 0 {
		return fmt.Errorf("%w: credit_limit 不能为负数：%q", ErrInvalidArgument, s)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, in repo.CreateInput) (*repo.Customer, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name 不能为空", ErrInvalidArgument)
	}
	if err := validateCreditLimit(in.CreditLimit); err != nil {
		return nil, err
	}
	c, err := s.repo.Create(ctx, in)
	if err != nil {
		s.logger.Error("创建客户失败", "code", in.Code, "error", err)
		return nil, err
	}
	return c, nil
}

type UpdateInput struct {
	IdempotencyKey string
	ID             string
	Version        int64
	Name           string
	TaxNo          string
	CreditLimit    string
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (*repo.Customer, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name 不能为空", ErrInvalidArgument)
	}
	if err := validateCreditLimit(in.CreditLimit); err != nil {
		return nil, err
	}
	c, err := s.repo.Update(ctx, repo.UpdateInput{
		IdempotencyKey: in.IdempotencyKey,
		ID:             in.ID,
		Version:        in.Version,
		Name:           in.Name,
		TaxNo:          in.TaxNo,
		CreditLimit:    in.CreditLimit,
	})
	if err != nil {
		s.logger.Error("更新客户失败", "id", in.ID, "error", err)
		return nil, err
	}
	return c, nil
}

type SetStatusInput struct {
	IdempotencyKey string
	ID             string
	Version        int64
	Status         string
}

func (s *Service) SetStatus(ctx context.Context, in SetStatusInput) (*repo.Customer, error) {
	c, err := s.repo.SetStatus(ctx, repo.SetStatusInput{
		IdempotencyKey: in.IdempotencyKey,
		ID:             in.ID,
		Version:        in.Version,
		Status:         in.Status,
	})
	if err != nil {
		s.logger.Error("变更客户状态失败", "id", in.ID, "status", in.Status, "error", err)
		return nil, err
	}
	return c, nil
}

func (s *Service) AddContact(ctx context.Context, in repo.AddContactInput) (*repo.Contact, error) {
	c, err := s.repo.AddContact(ctx, in)
	if err != nil {
		s.logger.Error("新增联系人失败", "customer_id", in.CustomerID, "error", err)
		return nil, err
	}
	return c, nil
}

func (s *Service) Get(ctx context.Context, id string) (*repo.Customer, error) {
	got, _, err := s.repo.BatchGet(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return nil, repo.ErrNotFound
	}
	return got[0], nil
}

func (s *Service) BatchGet(ctx context.Context, ids []string) (found []*repo.Customer, missing []string, err error) {
	return s.repo.BatchGet(ctx, ids)
}

func (s *Service) List(ctx context.Context, in repo.ListInput) (*repo.ListResult, error) {
	return s.repo.List(ctx, in)
}
