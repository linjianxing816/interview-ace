-- 0004_llm_call_error_status.sql —— 给 llm_calls.status 加一个 'error'
--
-- 原约束只有 ok / timeout / invalid_json / retried，覆盖不了「网络错误、401、余额不足」这类。
-- 硬塞进 retried 会让统计失真：retried 应该表示「重试过」，不是「失败了」。

alter table llm_calls drop constraint llm_calls_status_check;

alter table llm_calls add constraint llm_calls_status_check
    check (status in ('ok', 'timeout', 'invalid_json', 'retried', 'error'));
