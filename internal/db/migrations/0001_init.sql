-- 0001_init.sql —— InterviewAce 初始 schema
--
-- 设计要点（面试会问的三处）：
--   1. turns 上 (interview_id, seq) 唯一约束 —— 并发重复提交靠数据库兜底，不是应用层先查再插
--   2. turns.parent_turn_id 自引用 —— 追问成树，能回答「这题为什么被追问」
--   3. llm_calls.status 落库 —— 防失控不是嘴上说的，有日志能查
--
-- 金额一律用整数（本表暂无金额）；时间一律 timestamptz，不用 timestamp。

create table users (
    id            bigserial primary key,
    email         text        not null unique,
    password_hash text        not null,
    created_at    timestamptz not null default now()
);

-- 一人可传多份简历（换岗投不同 JD 时用不同版本）
create table resumes (
    id           bigserial primary key,
    user_id      bigint      not null references users (id) on delete cascade,
    filename     text        not null,
    raw_text     text,                                   -- 提取出的纯文本
    parsed       jsonb,                                  -- 结构化：教育 / 项目 / 技能
    parse_status text        not null
        check (parse_status in ('ok', 'degraded', 'unsupported')),
    created_at   timestamptz not null default now()
);

create index resumes_user_created_idx on resumes (user_id, created_at desc);

-- 面试会话：状态机主体
create table interviews (
    id          bigserial   primary key,
    user_id     bigint      not null references users (id) on delete cascade,
    resume_id   bigint      not null references resumes (id),
    jd_text     text        not null,
    role_type   text        not null,                    -- backend / test / ai / ...
    status      text        not null
        check (status in ('created', 'asking', 'answering', 'scoring', 'done', 'aborted')),
    current_seq int         not null default 0,
    max_turns   int         not null default 12 check (max_turns between 1 and 50),
    report      jsonb,
    created_at  timestamptz not null default now(),
    finished_at timestamptz
);

create index interviews_user_created_idx on interviews (user_id, created_at desc);
-- 部分索引：定时任务只扫「进行中」的会话，不扫历史
create index interviews_active_idx on interviews (status)
    where status in ('asking', 'answering');

-- 每一轮问答。追问通过 parent_turn_id 挂到被追问的那一轮下面
create table turns (
    id             bigserial   primary key,
    interview_id   bigint      not null references interviews (id) on delete cascade,
    seq            int         not null check (seq > 0),
    parent_turn_id bigint      references turns (id),
    question       text        not null,
    question_type  text        not null
        check (question_type in ('tech', 'project', 'behavior', 'followup')),
    answer         text,
    asked_at       timestamptz not null default now(),
    answered_at    timestamptz,

    -- 并发重复提交（网络重试）的兜底：同一会话同一轮只允许一条
    unique (interview_id, seq)
);

create index turns_interview_seq_idx on turns (interview_id, seq);
create index turns_parent_idx on turns (parent_turn_id);

-- 评分：turn_id 为空表示整场总评
create table scores (
    id           bigserial primary key,
    interview_id bigint    not null references interviews (id) on delete cascade,
    turn_id      bigint    references turns (id) on delete cascade,
    dimension    text      not null
        check (dimension in ('accuracy', 'clarity', 'depth')),
    score        smallint  not null check (score between 1 and 5),
    comment      text,
    sample_count smallint  not null default 1 check (sample_count >= 1),
    created_at   timestamptz not null default now(),

    -- nulls not distinct：总评那一行 turn_id 为 null，
    -- 不加这个 PG 会认为每条 null 都不同，允许重复插入总评
    unique nulls not distinct (interview_id, turn_id, dimension)
);

-- LLM 调用日志：成本核算 + 故障排查 + 防失控的证据
create table llm_calls (
    id                bigserial   primary key,
    interview_id      bigint      references interviews (id) on delete set null,
    purpose           text        not null
        check (purpose in ('question', 'followup', 'score', 'parse')),
    prompt_tokens     int,
    completion_tokens int,
    latency_ms        int,
    status            text        not null
        check (status in ('ok', 'timeout', 'invalid_json', 'retried')),
    created_at        timestamptz not null default now()
);

create index llm_calls_interview_idx on llm_calls (interview_id, created_at desc);
