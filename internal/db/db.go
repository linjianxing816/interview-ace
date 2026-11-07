// Package db 管理 PostgreSQL 连接池与 schema 迁移。
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"interview-ace/internal/config"
)

// 连接池参数。数值不是拍脑袋：
// 单机单进程 + 3 周 MVP，并发量级是「一个人点页面」，10 条足够；
// 上限开太大反而会在 PG 侧堆 idle 连接，把 max_connections 吃光。
const (
	maxConns        = 10
	minConns        = 1
	maxConnLifetime = time.Hour
	maxConnIdleTime = 30 * time.Minute
	connectTimeout  = 5 * time.Second
	pingTimeout     = 5 * time.Second
)

// NewPool 建连接池并立刻 Ping 一次。
// 启动时就暴露问题，比等第一个请求进来才报错好——那时候错误已经绕了几层。
func NewPool(ctx context.Context, cfg config.DBConfig) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("解析数据库连接串失败: %w", err)
	}

	pc.MaxConns = maxConns
	pc.MinConns = minConns
	pc.MaxConnLifetime = maxConnLifetime
	pc.MaxConnIdleTime = maxConnIdleTime
	pc.ConnConfig.ConnectTimeout = connectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接数据库失败（确认 PG 已启动、库 %s 已创建）: %w", cfg.Name, err)
	}

	return pool, nil
}

// Health 供 readyz 使用：返回 nil 表示数据库此刻可用。
// 它每次真的 Ping，不读缓存——就绪探针的意义是「现在能不能服务」。
func Health(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("连接池未初始化")
	}

	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()

	return pool.Ping(ctx)
}

// Stats 供排查用：当前池里有多少连接、等待过多少次。
func Stats(pool *pgxpool.Pool) map[string]int64 {
	if pool == nil {
		return map[string]int64{}
	}
	s := pool.Stat()
	return map[string]int64{
		"acquired":                 int64(s.AcquiredConns()),
		"idle":                     int64(s.IdleConns()),
		"total":                    int64(s.TotalConns()),
		"max":                      int64(s.MaxConns()),
		"acquire_count":            s.AcquireCount(),
		"empty_acquire_wait_count": s.EmptyAcquireCount(),
	}
}
