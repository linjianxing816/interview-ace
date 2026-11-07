package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"interview-ace/internal/testutil/dbtest"
)

// mustUser 建一个用户，供需要外键的用例使用。
func mustUser(t *testing.T, ctx context.Context, u *Users, email string) User {
	t.Helper()
	user, err := u.Create(ctx, email, "hash-placeholder")
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return user
}

func TestUsers_Create(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)

	got, err := users.Create(ctx, "  Alice@Example.COM  ", "hashed")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if got.ID <= 0 {
		t.Errorf("ID = %d，应为正数", got.ID)
	}
	// 邮箱必须归一化成小写去空格，否则同一个邮箱能注册两次
	if got.Email != "alice@example.com" {
		t.Errorf("Email = %q，期望归一化为 alice@example.com", got.Email)
	}
	if got.PasswordHash != "hashed" {
		t.Errorf("PasswordHash = %q", got.PasswordHash)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt 未填充")
	}
}

// TestUsers_Create_重复邮箱返回ErrEmailTaken 验证唯一约束被翻译成领域错误，
// 而不是把 pgx 的 23505 直接抛给上层。
func TestUsers_Create_重复邮箱返回ErrEmailTaken(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)

	mustUser(t, ctx, users, "dup@example.com")

	_, err := users.Create(ctx, "DUP@example.com", "hashed") // 大小写不同，归一化后相同
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("期望 ErrEmailTaken，实际: %v", err)
	}
}

func TestUsers_ByEmail(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)

	created := mustUser(t, ctx, users, "find@example.com")

	t.Run("命中", func(t *testing.T) {
		got, err := users.ByEmail(ctx, "FIND@example.com") // 大小写不敏感
		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}
		if got.ID != created.ID || got.Email != "find@example.com" {
			t.Errorf("查到 %+v，期望 id=%d", got, created.ID)
		}
	})

	t.Run("未命中", func(t *testing.T) {
		_, err := users.ByEmail(ctx, "ghost@example.com")
		if !errors.Is(err, ErrUserNotFound) {
			t.Errorf("期望 ErrUserNotFound，实际: %v", err)
		}
	})
}

func TestUsers_ByID(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)

	created := mustUser(t, ctx, users, "byid@example.com")

	got, err := users.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if got.Email != created.Email {
		t.Errorf("Email = %q，期望 %q", got.Email, created.Email)
	}

	if _, err := users.ByID(ctx, 999999); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("不存在的 ID 应返回 ErrUserNotFound，实际: %v", err)
	}
}

func TestResumes_Create_解析结果为空时写NULL(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "r1@example.com")
	resumes := NewResumes(pool)

	got, created, err := resumes.CreateOrGet(ctx, u.ID, "cv.pdf", "简历正文", ContentHash("简历正文"), nil, ParseDegraded)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if !created {
		t.Error("首次上传应返回 created=true")
	}
	if got.ParseStatus != ParseDegraded {
		t.Errorf("ParseStatus = %q", got.ParseStatus)
	}
	if len(got.Parsed) != 0 {
		t.Errorf("parsed 应为 nil（数据库里是 NULL），实际: %s", got.Parsed)
	}
	if got.RawText != "简历正文" {
		t.Errorf("RawText = %q", got.RawText)
	}
}

func TestResumes_Create_带结构化结果(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "r2@example.com")
	resumes := NewResumes(pool)

	parsed := []byte(`{"name":"张三","skills":["Go"]}`)
	got, _, err := resumes.CreateOrGet(ctx, u.ID, "cv.docx", "正文", ContentHash("正文"), parsed, ParseOK)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if got.ParseStatus != ParseOK {
		t.Errorf("ParseStatus = %q，期望 ok", got.ParseStatus)
	}

	// 再查一次，确认 jsonb 能原样读回来
	again, err := resumes.ByID(ctx, got.ID)
	if err != nil {
		t.Fatalf("回查失败: %v", err)
	}
	if len(again.Parsed) == 0 {
		t.Fatal("parsed 读回来是空的")
	}
	if !strings.Contains(string(again.Parsed), `"name"`) || !strings.Contains(string(again.Parsed), "张三") {
		t.Errorf("parsed 内容不对: %s", again.Parsed)
	}
}

// TestResumes_ListByUser_只返回自己的 是权限相关的数据隔离断言：
// SQL 里漏掉 user_id 条件的话，用户会看到别人的简历列表。
func TestResumes_ListByUser_只返回自己的(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)
	resumes := NewResumes(pool)

	alice := mustUser(t, ctx, users, "alice2@example.com")
	bob := mustUser(t, ctx, users, "bob2@example.com")

	if _, _, err := resumes.CreateOrGet(ctx, alice.ID, "alice1.pdf", "a1", ContentHash("a1"), nil, ParseDegraded); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, _, err := resumes.CreateOrGet(ctx, alice.ID, "alice2.pdf", "a2", ContentHash("a2"), nil, ParseDegraded); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, _, err := resumes.CreateOrGet(ctx, bob.ID, "bob1.pdf", "b1", ContentHash("b1"), nil, ParseDegraded); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	got, err := resumes.ListByUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("alice 应看到 2 条，实际 %d 条: %+v", len(got), got)
	}
	for _, r := range got {
		if r.UserID != alice.ID {
			t.Errorf("列表里混进了别人的简历: user_id=%d", r.UserID)
		}
	}

	bobList, err := resumes.ListByUser(ctx, bob.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(bobList) != 1 {
		t.Errorf("bob 应看到 1 条，实际 %d 条", len(bobList))
	}
}

func TestResumes_ListByUser_空列表不是nil(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "empty@example.com")

	got, err := NewResumes(pool).ListByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("期望空列表，实际 %d 条", len(got))
	}
}

func TestResumes_ByID_未命中(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()

	_, err := NewResumes(pool).ByID(ctx, 999999)
	if !errors.Is(err, ErrResumeNotFound) {
		t.Errorf("期望 ErrResumeNotFound，实际: %v", err)
	}
}

func TestParseCache_未命中返回ErrCacheMiss(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()

	_, err := NewParseCache(pool).Get(ctx, "不存在的键")
	if !errors.Is(err, ErrCacheMiss) {
		t.Errorf("期望 ErrCacheMiss，实际: %v", err)
	}
}

func TestParseCache_写入后能读回(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	cache := NewParseCache(pool)

	want := []byte(`{"name":"李四"}`)
	if err := cache.Put(ctx, "key-1", "deepseek-flash", want); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := cache.Get(ctx, "key-1")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.Contains(string(got), `"name"`) || !strings.Contains(string(got), "李四") {
		t.Errorf("读回的内容不对: %s", got)
	}
}

// TestParseCache_重复写入覆盖 验证 on conflict 分支：
// 两个请求同时解析同一份简历时，后写的那个不该报错。
func TestParseCache_重复写入覆盖(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	cache := NewParseCache(pool)

	if err := cache.Put(ctx, "key-2", "model-a", []byte(`{"name":"旧"}`)); err != nil {
		t.Fatalf("第一次写入失败: %v", err)
	}
	if err := cache.Put(ctx, "key-2", "model-b", []byte(`{"name":"新"}`)); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}

	got, err := cache.Get(ctx, "key-2")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.Contains(string(got), "新") {
		t.Errorf("应读到覆盖后的值，实际: %s", got)
	}
}

// TestResumes_CreateOrGet_相同内容返回已有记录 是去重的核心断言。
// 去重按内容哈希，不按文件名：换个名字重传同一份简历也应该被认出来。
func TestResumes_CreateOrGet_相同内容返回已有记录(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "dedup@example.com")
	resumes := NewResumes(pool)

	const body = "完全相同的简历正文"
	hash := ContentHash(body)

	first, created, err := resumes.CreateOrGet(ctx, u.ID, "cv.pdf", body, hash, nil, ParseDegraded)
	if err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if !created {
		t.Error("首次应 created=true")
	}

	// 换文件名、内容相同
	second, created2, err := resumes.CreateOrGet(ctx, u.ID, "cv-rename.pdf", body, hash, nil, ParseDegraded)
	if err != nil {
		t.Fatalf("重复创建失败: %v", err)
	}
	if created2 {
		t.Error("重复上传应 created=false")
	}
	if second.ID != first.ID {
		t.Errorf("应返回同一条记录，实际 id %d vs %d", second.ID, first.ID)
	}

	list, err := resumes.ListByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("库里应只有 1 条，实际 %d 条", len(list))
	}
}

// TestResumes_CreateOrGet_不同用户互不影响 验证唯一约束是 (user_id, hash) 而不是只有 hash：
// 两个人传同一份模板简历，各自都该有一条记录。
func TestResumes_CreateOrGet_不同用户互不影响(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)
	resumes := NewResumes(pool)

	alice := mustUser(t, ctx, users, "d1@example.com")
	bob := mustUser(t, ctx, users, "d2@example.com")

	const body = "同一份模板简历正文"
	hash := ContentHash(body)

	a, createdA, err := resumes.CreateOrGet(ctx, alice.ID, "t.pdf", body, hash, nil, ParseDegraded)
	if err != nil {
		t.Fatalf("alice 创建失败: %v", err)
	}
	b, createdB, err := resumes.CreateOrGet(ctx, bob.ID, "t.pdf", body, hash, nil, ParseDegraded)
	if err != nil {
		t.Fatalf("bob 创建失败: %v", err)
	}

	if !createdA || !createdB {
		t.Errorf("两个用户都应 created=true，实际 %v / %v", createdA, createdB)
	}
	if a.ID == b.ID {
		t.Error("不同用户应各自一条记录")
	}
}

func TestContentHash_相同内容相同哈希_不同内容不同哈希(t *testing.T) {
	a := ContentHash("简历正文 A")
	b := ContentHash("简历正文 A")
	c := ContentHash("简历正文 B")

	if a != b {
		t.Error("相同内容应得到相同哈希")
	}
	if a == c {
		t.Error("不同内容应得到不同哈希")
	}
	if len(a) != 64 {
		t.Errorf("sha256 十六进制应为 64 字符，实际 %d", len(a))
	}
}

// TestResumes_CreateOrGet_降级记录被新解析结果覆盖 覆盖一个真实场景：
// 第一次解析失败（degraded），用户重新上传，这次解析成功（ok）。
// 去重不能把重试的路堵死——新结果必须覆盖旧的降级记录。
func TestResumes_CreateOrGet_降级记录被新解析结果覆盖(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "retry@example.com")
	resumes := NewResumes(pool)

	const body = "同一份简历正文"
	hash := ContentHash(body)

	// 第一次：解析失败
	first, _, err := resumes.CreateOrGet(ctx, u.ID, "cv.pdf", body, hash, nil, ParseDegraded)
	if err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if first.ParseStatus != ParseDegraded {
		t.Fatalf("首次应为 degraded，实际 %q", first.ParseStatus)
	}

	// 第二次：同样内容，这次解析成功
	parsed := []byte(`{"name":"张三"}`)
	second, created, err := resumes.CreateOrGet(ctx, u.ID, "cv.pdf", body, hash, parsed, ParseOK)
	if err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if created {
		t.Error("内容相同应 created=false")
	}
	if second.ID != first.ID {
		t.Errorf("应是同一条记录，id %d vs %d", second.ID, first.ID)
	}
	if second.ParseStatus != ParseOK {
		t.Errorf("状态应升级为 ok，实际 %q", second.ParseStatus)
	}
	if !strings.Contains(string(second.Parsed), "张三") {
		t.Errorf("新的解析结果没写进去: %s", second.Parsed)
	}

	// 库里也只有一条
	list, _ := resumes.ListByUser(ctx, u.ID)
	if len(list) != 1 {
		t.Errorf("应只有 1 条，实际 %d 条", len(list))
	}
}

// TestResumes_CreateOrGet_成功的记录不被降级覆盖 是反向保护：
// 已有 ok 的记录，后面来一次失败的解析不能把它降级。
func TestResumes_CreateOrGet_成功的记录不被降级覆盖(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "nodowngrade@example.com")
	resumes := NewResumes(pool)

	const body = "另一份简历正文"
	hash := ContentHash(body)

	if _, _, err := resumes.CreateOrGet(ctx, u.ID, "cv.pdf", body, hash, []byte(`{"name":"李四"}`), ParseOK); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 再来一次解析失败
	got, _, err := resumes.CreateOrGet(ctx, u.ID, "cv.pdf", body, hash, nil, ParseDegraded)
	if err != nil {
		t.Fatalf("重复创建失败: %v", err)
	}
	if got.ParseStatus != ParseOK {
		t.Errorf("已有 ok 的记录不该被降级，实际 %q", got.ParseStatus)
	}
	if !strings.Contains(string(got.Parsed), "李四") {
		t.Errorf("旧的解析结果不该被清掉: %s", got.Parsed)
	}
}

// TestParseCache_PurgeOlderThan 验证缓存清理：
// 只删过期的，新记录不动。缓存只写不删会一直长。
func TestParseCache_PurgeOlderThan(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	cache := NewParseCache(pool)

	if err := cache.Put(ctx, "old-key", "m", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := cache.Put(ctx, "new-key", "m", []byte(`{"b":2}`)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	// 把 old-key 的创建时间改到 40 天前
	if _, err := pool.Exec(ctx,
		`update parse_cache set created_at = now() - interval '40 days' where text_hash = 'old-key'`); err != nil {
		t.Fatalf("调整时间失败: %v", err)
	}

	removed, err := cache.PurgeOlderThan(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if removed != 1 {
		t.Errorf("应删除 1 条，实际 %d 条", removed)
	}

	if _, err := cache.Get(ctx, "old-key"); !errors.Is(err, ErrCacheMiss) {
		t.Errorf("过期记录应被删除，实际: %v", err)
	}
	if _, err := cache.Get(ctx, "new-key"); err != nil {
		t.Errorf("新记录不该被删，实际: %v", err)
	}
}

func TestParseCache_PurgeOlderThan_没有过期记录时不删(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	cache := NewParseCache(pool)

	if err := cache.Put(ctx, "k", "m", []byte(`{}`)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	removed, err := cache.PurgeOlderThan(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if removed != 0 {
		t.Errorf("不该删任何记录，实际删了 %d 条", removed)
	}
}

// TestLLMCalls_Record与Summary 验证用量能被记录并聚合。
// 这张表如果没人写，"成本能核算"就是空话。
func TestLLMCalls_Record与Summary(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	calls := NewLLMCalls(pool)

	records := []LLMCall{
		{Purpose: PurposeParse, PromptTokens: 100, CompletionTokens: 20, LatencyMS: 300, Status: StatusOK},
		{Purpose: PurposeParse, PromptTokens: 200, CompletionTokens: 30, LatencyMS: 400, Status: StatusOK},
		{Purpose: PurposeParse, PromptTokens: 50, CompletionTokens: 0, LatencyMS: 900, Status: StatusInvalidJSON},
	}
	for _, r := range records {
		if err := calls.Record(ctx, r); err != nil {
			t.Fatalf("记录失败: %v", err)
		}
	}

	sum, err := calls.Summary(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}

	if sum.TotalCalls != 3 {
		t.Errorf("TotalCalls = %d，期望 3", sum.TotalCalls)
	}
	// 100+20 + 200+30 + 50+0 = 400
	if sum.TotalTokens != 400 {
		t.Errorf("TotalTokens = %d，期望 400", sum.TotalTokens)
	}
	if sum.FailedCalls != 1 {
		t.Errorf("FailedCalls = %d，期望 1", sum.FailedCalls)
	}
	if len(sum.ByPurpose) != 1 || sum.ByPurpose[0].Purpose != PurposeParse {
		t.Fatalf("按用途分组不对: %+v", sum.ByPurpose)
	}
	if sum.ByPurpose[0].Calls != 3 {
		t.Errorf("parse 调用数 = %d，期望 3", sum.ByPurpose[0].Calls)
	}
}

func TestLLMCalls_Summary只统计时间窗口内(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	calls := NewLLMCalls(pool)

	if err := calls.Record(ctx, LLMCall{Purpose: PurposeParse, PromptTokens: 10, Status: StatusOK}); err != nil {
		t.Fatalf("记录失败: %v", err)
	}
	// 把它挪到 60 天前
	if _, err := pool.Exec(ctx,
		`update llm_calls set created_at = now() - interval '60 days'`); err != nil {
		t.Fatalf("调整时间失败: %v", err)
	}

	sum, err := calls.Summary(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	if sum.TotalCalls != 0 {
		t.Errorf("30 天窗口内不该有记录，实际 %d 条", sum.TotalCalls)
	}
}

func TestLLMCalls_带面试ID(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "llm@example.com")
	resumes := NewResumes(pool)

	r, _, err := resumes.CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	if err != nil {
		t.Fatalf("创建简历失败: %v", err)
	}
	var interviewID int64
	if err := pool.QueryRow(ctx,
		`insert into interviews (user_id, resume_id, jd_text, role_type, status)
		 values ($1, $2, 'jd', 'backend', 'created') returning id`, u.ID, r.ID).Scan(&interviewID); err != nil {
		t.Fatalf("创建面试失败: %v", err)
	}

	calls := NewLLMCalls(pool)
	if err := calls.Record(ctx, LLMCall{
		InterviewID: &interviewID, Purpose: PurposeQuestion,
		PromptTokens: 500, CompletionTokens: 100, Status: StatusOK,
	}); err != nil {
		t.Fatalf("记录失败: %v", err)
	}

	sum, err := calls.Summary(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("汇总失败: %v", err)
	}
	if sum.TotalTokens != 600 {
		t.Errorf("TotalTokens = %d，期望 600", sum.TotalTokens)
	}
}

// TestUsers_Exists 是认证中间件的依赖：每个请求都要问一次「这个用户还在吗」。
// 存在的用户 true、不存在的 false、删掉之后变 false。
func TestUsers_Exists(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)

	u := mustUser(t, ctx, users, "exists@example.com")

	ok, err := users.Exists(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !ok {
		t.Error("存在的用户应返回 true")
	}

	// 不存在的 id
	ok, err = users.Exists(ctx, 999999)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if ok {
		t.Error("不存在的用户应返回 false")
	}

	// 删掉之后（级联会清掉简历等）
	if _, err := pool.Exec(ctx, `delete from users where id = $1`, u.ID); err != nil {
		t.Fatalf("删除用户失败: %v", err)
	}
	ok, err = users.Exists(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if ok {
		t.Error("删除后应返回 false")
	}
}
