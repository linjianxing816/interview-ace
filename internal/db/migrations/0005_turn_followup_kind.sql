-- 0005_turn_followup_kind.sql —— 记录追问属于哪一类
--
-- 为什么要单独一列而不是塞进 question_type：
--   question_type 回答的是「这题属于什么话题」（tech/project/behavior/followup），
--   followup_kind 回答的是「这题在挖什么」（归因/递进/价值/盲区/验证/边界/权衡）。
--   两个维度正交，合并成一个字段就没法按「话题 × 追问类型」交叉统计。
--
-- 允许为 null：非追问的题目没有类型，硬填一个默认值会污染统计。

alter table turns add column followup_kind text
    check (followup_kind in ('cause', 'probe', 'value', 'blindspot', 'verify', 'boundary', 'tradeoff'));

-- 报告里要按类型聚合「哪类追问下失分最多」，走这个索引
create index turns_followup_kind_idx on turns (interview_id, followup_kind)
    where followup_kind is not null;
