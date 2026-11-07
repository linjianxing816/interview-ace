package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 集成测试：需要真实 PostgreSQL。
// 用法：先设 TEST_DB_DSN（指向一个有建库权限的库），没设就跳过。
//
//	TEST_DB_DSN="postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable" go test ./internal/db -v
//
// 每个用例建一个临时库，跑完删掉，不污染开发库。
func TestMigrate_建表与幂等(t *testing.T) {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("未设置 TEST_DB_DSN，跳过集成测试")
	}

	ctx := context.Background()
	name := fmt.Sprintf("ace_test_%d", time.Now().UnixNano())

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连接管理库失败: %v", err)
	}
	defer admin.Close()

	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		t.Fatalf("建临时库失败: %v", err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "drop database if exists "+name+" with (force)"); err != nil {
			t.Logf("清理临时库 %s 失败: %v", name, err)
		}
	}()

	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("解析 DSN 失败: %v", err)
	}
	pc.ConnConfig.Database = name

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatalf("连接临时库失败: %v", err)
	}
	defer pool.Close()

	// ---- 第一次迁移：应执行 0001 ----
	done, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	wantMigrations := []string{"0001_init", "0002_parse_cache", "0003_resume_dedup",
		"0004_llm_call_error_status", "0005_turn_followup_kind"}
	if strings.Join(done, ",") != strings.Join(wantMigrations, ",") {
		t.Fatalf("首次迁移应执行 %v，实际 %v", wantMigrations, done)
	}

	// ---- 六张业务表 + schema_migrations 都在 ----
	want := []string{"interviews", "llm_calls", "parse_cache", "resumes", "schema_migrations", "scores", "turns", "users"}
	var got []string
	rows, err := pool.Query(ctx,
		`select table_name from information_schema.tables
		 where table_schema = 'public' order by table_name`)
	if err != nil {
		t.Fatalf("查询表失败: %v", err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("扫描表名失败: %v", err)
		}
		got = append(got, s)
	}
	rows.Close()

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("表清单不符\n实际: %v\n期望: %v", got, want)
	}

	// ---- 第二次迁移：幂等，什么都不执行 ----
	again, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("重复迁移失败: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("重复迁移应返回空，实际 %v", again)
	}

	// ---- 核心约束真的生效：同一会话同一轮只能有一条 ----
	assertDuplicateTurnRejected(t, ctx, pool)
}

// assertDuplicateTurnRejected 验证 (interview_id, seq) 唯一约束在库层拦住并发重复提交。
// 这是「不是应用层先查再插」的证据——绕过应用直接写库也插不进去。
func assertDuplicateTurnRejected(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	var userID, resumeID, interviewID int64
	if err := pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, 'x') returning id`,
		fmt.Sprintf("t%d@example.com", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatalf("插入用户失败: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`insert into resumes (user_id, filename, parse_status) values ($1, 'cv.pdf', 'ok') returning id`,
		userID).Scan(&resumeID); err != nil {
		t.Fatalf("插入简历失败: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`insert into interviews (user_id, resume_id, jd_text, role_type, status)
		 values ($1, $2, 'jd', 'backend', 'asking') returning id`,
		userID, resumeID).Scan(&interviewID); err != nil {
		t.Fatalf("插入面试失败: %v", err)
	}

	insert := `insert into turns (interview_id, seq, question, question_type) values ($1, 1, 'q', 'tech')`
	if _, err := pool.Exec(ctx, insert, interviewID); err != nil {
		t.Fatalf("首次插入轮次失败: %v", err)
	}

	_, err := pool.Exec(ctx, insert, interviewID)
	if err == nil {
		t.Fatal("同一 (interview_id, seq) 重复插入应被拒绝，实际成功了")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("期望唯一约束冲突 (23505)，实际: %v", err)
	}
}
