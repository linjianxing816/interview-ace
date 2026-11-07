// Package dbtest 给需要真实数据库的测试提供一次性测试库。
//
// 单独成包而不是塞进 testutil：它要 import internal/db 跑迁移，
// 而 db 包的测试（package db）如果 import 了 testutil 就形成循环依赖。
package dbtest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"interview-ace/internal/db"
)

// NewPool 建一个临时数据库、跑完迁移、返回连上它的连接池。
// 测试结束时自动删库。
//
// 没设 TEST_DB_DSN 就跳过——CI 里不配数据库也能跑单元测试。
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_DB_DSN，跳过数据库集成测试")
	}

	ctx := context.Background()
	name := fmt.Sprintf("ace_store_%d", time.Now().UnixNano())

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连接管理库失败: %v", err)
	}
	t.Cleanup(admin.Close)

	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatalf("建临时库失败: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(),
			"drop database if exists "+name+" with (force)"); err != nil {
			t.Logf("清理临时库 %s 失败: %v", name, err)
		}
	})

	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("解析 DSN 失败: %v", err)
	}
	pc.ConnConfig.Database = name

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatalf("连接临时库失败: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	return pool
}
