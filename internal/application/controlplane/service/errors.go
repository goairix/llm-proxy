package service

import (
	"errors"

	controlerrors "github.com/goairix/llm-proxy/internal/application/controlplane/errors"
	sharederrors "github.com/goairix/llm-proxy/internal/domain/shared/errors"
)

func mapApplicationError(err error) error {
	if err == nil {
		return nil
	}
	var applicationError *controlerrors.Error
	if errors.As(err, &applicationError) {
		return err
	}
	switch {
	case errors.Is(err, sharederrors.ErrInvalid):
		return controlerrors.New(controlerrors.InvalidRequest, "请求参数不合法", "", err)
	case errors.Is(err, sharederrors.ErrConflict):
		return controlerrors.New(controlerrors.Conflict, "资源状态冲突", "", err)
	case errors.Is(err, sharederrors.ErrDependencyUnavailable):
		return controlerrors.New(controlerrors.DependencyUnavailable, "依赖服务暂不可用", "", err)
	default:
		return controlerrors.New(controlerrors.Internal, "服务内部错误", "", err)
	}
}

func notFound(resource string) error {
	return controlerrors.New(controlerrors.NotFound, resource+"不存在", "", nil)
}

func disabled(resource string) error {
	return controlerrors.New(controlerrors.Conflict, resource+"已停用", "", nil)
}
