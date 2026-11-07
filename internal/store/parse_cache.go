package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCacheMiss 表示缓存里没有这条记录。它不算错误，是正常的未命中。
var ErrCacheMiss = errors.New("缓存未命中")

// ParseCache 是简历解析结果的缓存表。
type ParseCache struct {
	pool *pgxpool.Pool
}

func NewParseCache(pool *pgxpool.Pool) *ParseCache {
	return &ParseCache{pool: pool}
}

// Get 取缓存。未命中返回 ErrCacheMiss，调用方据此决定要不要真的去解析。
func (c *ParseCache) Get(ctx context.Context, key string) ([]byte, error) {
	var parsed []byte
	err := c.pool.QueryRow(ctx,
		`select parsed from parse_cache where text_hash = $1`, key,
	).Scan(&parsed)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCacheMiss
		}
		return nil, fmt.Errorf("读取解析缓存失败: %w", err)
	}

	return parsed, nil
}

// Put 写缓存。同一个 key 重复写用覆盖，不报错——
// 两个请求同时解析同一份简历时，后写的那个不该失败。
func (c *ParseCache) Put(ctx context.Context, key, model string, parsed []byte) error {
	_, err := c.pool.Exec(ctx,
		`insert into parse_cache (text_hash, model, parsed) values ($1, $2, $3)
		 on conflict (text_hash) do update set parsed = excluded.parsed, model = excluded.model`,
		key, model, parsed)
	if err != nil {
		return fmt.Errorf("写入解析缓存失败: %w", err)
	}
	return nil
}

// PurgeOlderThan 删掉超过 keep 的缓存记录，返回删除条数。
//
// 缓存只写不删会一直长。项目没有常驻调度器，所以由服务启动时调一次——
// 单机单进程的部署方式下，这已经够用了。
func (c *ParseCache) PurgeOlderThan(ctx context.Context, keep time.Duration) (int64, error) {
	cutoff := time.Now().Add(-keep)

	tag, err := c.pool.Exec(ctx,
		`delete from parse_cache where created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("清理解析缓存失败: %w", err)
	}
	return tag.RowsAffected(), nil
}
