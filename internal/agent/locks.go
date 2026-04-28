/*
Copyright 2026 BUPT AIOps Lab.
*/

package agent

import (
	"fmt"
	"sync"
)

// LinkLocks 给每条 (namespace, uid) 维度的链路提供 per-link 互斥锁。
//
// 使用方式：
//
//	unlock := locks.Lock("default", 42)
//	defer unlock()
//	// ... 操作 vh42/vx42/br42/A_intf ...
//
// 这是 p2pnet 现有设计的复刻；同节点上同一 uid 的并发 make/delete 会被串行化。
type LinkLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewLinkLocks 创建一个空的 LinkLocks。
func NewLinkLocks() *LinkLocks {
	return &LinkLocks{locks: map[string]*sync.Mutex{}}
}

// Lock 锁定指定 link 并返回 unlock 闭包。
func (l *LinkLocks) Lock(namespace string, uid int64) func() {
	key := fmt.Sprintf("%s/%d", namespace, uid)

	l.mu.Lock()
	m, ok := l.locks[key]
	if !ok {
		m = &sync.Mutex{}
		l.locks[key] = m
	}
	l.mu.Unlock()

	m.Lock()
	return m.Unlock
}
