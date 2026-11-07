-- 0002_parse_cache.sql —— 简历解析结果缓存
--
-- 为什么需要：同一份简历反复上传（改个文件名重传、或者解析失败后重试）
-- 会重复调模型。一次解析约 2000+300 token，缓存命中就省下来了。
--
-- 键用 sha256(模型名 + 正文)：带上模型名是必须的——
-- 换了模型之后旧结果不能复用，否则会拿到别的模型的结构化结果。

create table parse_cache (
    text_hash  text        primary key,
    model      text        not null,
    parsed     jsonb       not null,
    created_at timestamptz not null default now()
);

-- 定期清理用：缓存不该无限增长
create index parse_cache_created_idx on parse_cache (created_at);
