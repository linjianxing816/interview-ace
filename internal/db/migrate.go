package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 迁移文件编译进二进制。好处是不依赖运行时工作目录——
// 用 os.ReadDir("migrations") 的话，从别的目录启动就找不到文件。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

const createMigrationsTable = `
create table if not exists schema_migrations (
    version    text primary key,
    applied_at timestamptz not null default now()
)`

// Migrate 按文件名顺序执行未应用的迁移，每个文件一个事务。
// 返回本次实际应用的版本号列表；重复调用是幂等的（已应用的跳过）。
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	if pool == nil {
		return nil, fmt.Errorf("连接池未初始化")
	}

	if _, err := pool.Exec(ctx, createMigrationsTable); err != nil {
		return nil, fmt.Errorf("创建 schema_migrations 表失败: %w", err)
	}

	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}

	files, err := migrationFiles()
	if err != nil {
		return nil, err
	}

	var done []string
	for _, name := range files {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return done, fmt.Errorf("读取迁移 %s 失败: %w", name, err)
		}

		if err := applyOne(ctx, pool, version, string(body)); err != nil {
			return done, err
		}
		done = append(done, version)
	}

	return done, nil
}

// applyOne 在一个事务里执行整个 .sql 文件并登记版本号。
// 中途失败就整体回滚，不会留下「建了一半的表」。
func applyOne(ctx context.Context, pool *pgxpool.Pool, version, body string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败（%s）: %w", version, err)
	}
	// 显式 Commit 之前任何路径退出都会走到这里，重复 Rollback 是安全的空操作
	defer func() { _ = tx.Rollback(ctx) }()

	// 一个 .sql 文件里有多条语句，必须走简单协议。
	// pgx 默认用扩展协议，扩展协议一次只允许一条语句。
	if _, err := tx.Exec(ctx, body, pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("执行迁移 %s 失败: %w", version, err)
	}

	if _, err := tx.Exec(ctx, `insert into schema_migrations (version) values ($1)`, version); err != nil {
		return fmt.Errorf("登记迁移版本 %s 失败: %w", version, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交迁移 %s 失败: %w", version, err)
	}

	return nil
}

func appliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `select version from schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("查询已应用迁移失败: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("读取迁移版本失败: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历迁移版本失败: %w", err)
	}

	return applied, nil
}

func migrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录失败: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}

	// 文件名前缀即执行顺序（0001_ / 0002_ ...）
	sort.Strings(names)
	return names, nil
}
