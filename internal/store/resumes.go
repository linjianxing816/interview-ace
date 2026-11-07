package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// parse_status 的取值。跟数据库的 check 约束一一对应，
// 改这里必须同时改 migrations 里的约束，否则插入会被库拒绝。
const (
	ParseOK          = "ok"
	ParseDegraded    = "degraded"
	ParseUnsupported = "unsupported"
)

// ErrResumeNotFound 查不到简历。
var ErrResumeNotFound = errors.New("简历不存在")

// Resume 是 resumes 表的一行。
type Resume struct {
	ID          int64
	UserID      int64
	Filename    string
	RawText     string
	Parsed      json.RawMessage // 结构化结果，可能为 nil
	ParseStatus string
	ContentHash string
	CreatedAt   time.Time
}

// ContentHash 算简历正文的内容指纹，用于同用户去重。
// 用正文而不是文件名：同一份简历换个名字重传很常见。
func ContentHash(rawText string) string {
	sum := sha256.Sum256([]byte(rawText))
	return hex.EncodeToString(sum[:])
}

// Resumes 是 resumes 表的访问器。
type Resumes struct {
	pool *pgxpool.Pool
}

func NewResumes(pool *pgxpool.Pool) *Resumes {
	return &Resumes{pool: pool}
}

// CreateOrGet 保存一份解析过的简历。
//
// 同一用户传过完全相同的内容时直接返回旧记录，created 为 false——
// 去重靠 (user_id, content_hash) 的唯一索引，不是「先查再插」（那有 TOCTOU 窗口）。
//
// parsed 传 nil 时写 NULL（解析失败的情况），不写 "null" 字符串——
// 两者在 jsonb 里语义不同，后者会让下游以为解析出了个 null 对象。
func (r *Resumes) CreateOrGet(ctx context.Context, userID int64, filename, rawText, contentHash string, parsed []byte, status string) (Resume, bool, error) {
	var out Resume
	var parsedArg any
	if len(parsed) > 0 {
		parsedArg = parsed
	}

	err := r.pool.QueryRow(ctx,
		`insert into resumes (user_id, filename, raw_text, parsed, parse_status, content_hash)
		 values ($1, $2, $3, $4, $5, $6)
		 -- 索引是部分索引（where content_hash is not null），
		 -- ON CONFLICT 必须重复同一个谓词，否则 PG 报 42P10
		 on conflict (user_id, content_hash) where content_hash is not null do nothing
		 returning id, user_id, filename, coalesce(raw_text, ''), parsed, parse_status, created_at`,
		userID, filename, rawText, parsedArg, status, contentHash,
	).Scan(&out.ID, &out.UserID, &out.Filename, &out.RawText, &out.Parsed, &out.ParseStatus, &out.CreatedAt)

	if err == nil {
		return out, true, nil
	}
	// on conflict do nothing + returning 在冲突时不返回任何行，落到 ErrNoRows
	if !errors.Is(err, pgx.ErrNoRows) {
		return Resume{}, false, fmt.Errorf("保存简历失败: %w", err)
	}

	existing, err := r.byUserAndHash(ctx, userID, contentHash)
	if err != nil {
		return Resume{}, false, err
	}

	// 已有记录是降级状态、这次解析成功了 → 用新结果覆盖。
	// 不这么做的话，「第一次解析失败、重新上传解析成功」的新结果会被丢掉，
	// 用户永远卡在 degraded 上（去重把重试的路也堵了）。
	if status == ParseOK && existing.ParseStatus != ParseOK && len(parsed) > 0 {
		updated, err := r.updateParsed(ctx, existing.ID, parsed, status)
		if err != nil {
			return Resume{}, false, err
		}
		return updated, false, nil
	}

	return existing, false, nil
}

// updateParsed 用新的解析结果覆盖已有记录。
func (r *Resumes) updateParsed(ctx context.Context, id int64, parsed []byte, status string) (Resume, error) {
	var out Resume
	err := r.pool.QueryRow(ctx,
		`update resumes set parsed = $2, parse_status = $3 where id = $1
		 returning id, user_id, filename, coalesce(raw_text, ''), parsed, parse_status, created_at`,
		id, parsed, status,
	).Scan(&out.ID, &out.UserID, &out.Filename, &out.RawText, &out.Parsed, &out.ParseStatus, &out.CreatedAt)

	if err != nil {
		return Resume{}, fmt.Errorf("更新解析结果失败: %w", err)
	}
	return out, nil
}

// byUserAndHash 按 (用户, 内容哈希) 查已有记录。只被 CreateOrGet 的冲突分支调用。
func (r *Resumes) byUserAndHash(ctx context.Context, userID int64, hash string) (Resume, error) {
	var out Resume
	err := r.pool.QueryRow(ctx,
		`select id, user_id, filename, coalesce(raw_text, ''), parsed, parse_status, created_at
		 from resumes where user_id = $1 and content_hash = $2`, userID, hash,
	).Scan(&out.ID, &out.UserID, &out.Filename, &out.RawText, &out.Parsed, &out.ParseStatus, &out.CreatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resume{}, ErrResumeNotFound
		}
		return Resume{}, fmt.Errorf("查询已有简历失败: %w", err)
	}
	return out, nil
}

// ListByUser 按时间倒序列出用户的简历（不含正文，列表页不需要）。
func (r *Resumes) ListByUser(ctx context.Context, userID int64) ([]Resume, error) {
	rows, err := r.pool.Query(ctx,
		`select id, user_id, filename, parse_status, created_at
		 from resumes where user_id = $1 order by created_at desc, id desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询简历列表失败: %w", err)
	}
	defer rows.Close()

	var out []Resume
	for rows.Next() {
		var it Resume
		if err := rows.Scan(&it.ID, &it.UserID, &it.Filename, &it.ParseStatus, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取简历行失败: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历简历列表失败: %w", err)
	}

	return out, nil
}

// ByID 查单份简历（含正文和结构化结果）。
// 调用方必须自己校验 user_id——这里只按主键查，不做权限判断。
func (r *Resumes) ByID(ctx context.Context, id int64) (Resume, error) {
	var out Resume
	err := r.pool.QueryRow(ctx,
		`select id, user_id, filename, coalesce(raw_text, ''), parsed, parse_status, created_at
		 from resumes where id = $1`, id,
	).Scan(&out.ID, &out.UserID, &out.Filename, &out.RawText, &out.Parsed, &out.ParseStatus, &out.CreatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Resume{}, ErrResumeNotFound
		}
		return Resume{}, fmt.Errorf("查询简历失败: %w", err)
	}

	return out, nil
}
