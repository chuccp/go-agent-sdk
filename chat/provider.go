package chat

import (
	"context"
	"errors"
	"sync"
)

// ErrInvalidStream 响应体不是可解析的 SSE 流（整条流一个有效事件都没有）。
//
// 实测来源（2026-10-12）：deepseek 的 anthropic 兼容口偶发回**没声明编码的压缩体**
// （content-encoding 为空、内容却是二进制，约 7/8 命中），逐行找 `data: ` 一条都匹配不上，
// 以前会被静默当成"正常空流"——用户那边就是"说了话没回音"。provider 现在把这种情况
// 包成这个错误返回，**并在 HTTP 层自动重试**（同一份请求再 POST，见 anthropic 的
// ChatWithStream）；重试都用完还失败才把这个错误抛出来。
var ErrInvalidStream = errors.New("响应体不是可解析的 SSE 流（上游网关偶发，通常重试可过）")

// Service 是 LLM 提供方的流式对话接口。
// 每个 provider（如 OpenAI、Anthropic）实现此接口：往 BlockWriter 里写块，
// 不依赖具体的 BlockStream 实现。
type Service interface {
	ChatWithStream(ctx context.Context, chatMessages *Messages, response BlockWriter) error
	ID() string
}

// ServiceStore 管理多个 LLM provider 的注册与路由。
type ServiceStore struct {
	serviceMap map[string]Service
	rLock      *sync.RWMutex
}

func NewServiceStore() *ServiceStore {
	return &ServiceStore{
		serviceMap: make(map[string]Service),
		rLock:      new(sync.RWMutex),
	}
}

func (r *ServiceStore) GetService(id string) Service {
	r.rLock.RLock()
	defer r.rLock.RUnlock()
	if r.serviceMap == nil {
		return nil
	}
	return r.serviceMap[id]
}

func (r *ServiceStore) Register(chatService Service) {
	r.rLock.Lock()
	defer r.rLock.Unlock()
	if r.serviceMap == nil {
		r.serviceMap = make(map[string]Service)
	}
	r.serviceMap[chatService.ID()] = chatService
}
