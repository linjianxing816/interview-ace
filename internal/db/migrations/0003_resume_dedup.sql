-- 0003_resume_dedup.sql —— 同一用户重复上传同一份简历时去重
--
-- 为什么需要：缓存只省了模型调用，记录还是会插两条。
-- 用户在列表里看到两个一模一样的文件名，不知道该点哪个。
--
-- 用内容哈希而不是文件名：同一份简历换个文件名重传很常见，
-- 反过来同名不同内容（改了版本）也应该算两条。

alter table resumes add column content_hash text;

-- 部分唯一索引：只约束有哈希的行。
-- 老数据（0003 之前插入的）哈希为空，不参与去重。
create unique index resumes_user_hash_uniq
    on resumes (user_id, content_hash)
    where content_hash is not null;
