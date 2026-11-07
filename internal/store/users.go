// Package store 是数据访问层。每个聚合一个文件，只放 SQL 和类型转换，
// 不放业务规则——规则在 handler 和纯函数里，这样才好单测。
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 领域错误。handler 根据它们决定状态码，不靠字符串匹配。
var (
	ErrEmailTaken   = errors.New("邮箱已被注册")
	ErrUserNotFound = errors.New("用户不存在")
)

// User 是 users 表的一行。
// 刻意不含 PasswordHash 之外的敏感字段；PasswordHash 也不带 json tag，
// 避免哪天有人直接把它塞进响应体。
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// Users 是 users 表的访问器。
type Users struct {
	pool *pgxpool.Pool
}

func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

// NormalizeEmail 统一小写并去空格。
// 在 store 层做，保证「A@x.com」和「a@x.com」不会变成两个账号。
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Create 插入新用户。
// 邮箱冲突靠数据库唯一索引兜底（并发下先查再插会漏），
// 这里把 23505 翻译成 ErrEmailTaken。
func (u *Users) Create(ctx context.Context, email, passwordHash string) (User, error) {
	email = NormalizeEmail(email)

	var out User
	err := u.pool.QueryRow(ctx,
		`insert into users (email, password_hash) values ($1, $2)
		 returning id, email, password_hash, created_at`,
		email, passwordHash,
	).Scan(&out.ID, &out.Email, &out.PasswordHash, &out.CreatedAt)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("创建用户失败: %w", err)
	}

	return out, nil
}

// ByEmail 按邮箱查用户。查不到返回 ErrUserNotFound。
func (u *Users) ByEmail(ctx context.Context, email string) (User, error) {
	return u.queryOne(ctx,
		`select id, email, password_hash, created_at from users where email = $1`,
		NormalizeEmail(email))
}

// ByID 按主键查用户。认证中间件用它确认令牌里的 user_id 还有效。
func (u *Users) ByID(ctx context.Context, id int64) (User, error) {
	return u.queryOne(ctx,
		`select id, email, password_hash, created_at from users where id = $1`, id)
}

// Exists 判断用户是否还在。
//
// 认证中间件每个请求都会调一次，所以只查一个 bool，不取整行——
// 不返回 email / password_hash 就少一次内存拷贝，也不用担心这些字段被误用。
//
// 存在的意义：令牌签发后用户可能被删（测试清库、账号注销）。
// 只验签名的话，这种令牌仍然能过，直到写库时撞外键才炸成 500。
func (u *Users) Exists(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := u.pool.QueryRow(ctx,
		`select exists(select 1 from users where id = $1)`, id).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("查询用户是否存在失败: %w", err)
	}
	return ok, nil
}

func (u *Users) queryOne(ctx context.Context, sql string, arg any) (User, error) {
	var out User
	err := u.pool.QueryRow(ctx, sql, arg).Scan(&out.ID, &out.Email, &out.PasswordHash, &out.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("查询用户失败: %w", err)
	}
	return out, nil
}
