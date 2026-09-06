package service

import (
	"errors"

	"github.com/brickKit/mdm-customer/backend/internal/repo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ToStatus 把 repo 层的哨兵错误翻成 gRPC status——HTTP 与 gRPC 两条对外
// 接口共用同一套业务错误类型，不用为两种协议各写一遍映射
// （be-sdk-go 的 recoveryAndErrorMappingMiddleware 已经把 gRPC status
// 错误转成对应的 HTTP 状态码，见 gin.go 的 grpcCodeToHTTPStatus）。
func ToStatus(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, repo.ErrVersionConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
