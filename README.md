# InterviewAce · AI 模拟面试平台（Go）

> 状态：**核心闭环已完成**（2026-11-02 ~ 11-04，3 天）——注册 → 传简历 → 开面试 → 追问 → 评分 → 报告 → 趋势，346 个测试全绿（含 43 个真库）
> 完整方案：`../00-前置文档/项目二-方案.md`
> 数据库：`mock_interview`（本机 PostgreSQL 16，`127.0.0.1:5432`）

求职者上传简历 + 目标岗位 JD → AI 扮演面试官多轮追问 → 出评分和复盘。

---

## 怎么跑

```bash
# 1. 配置（.env 已在本地，含密钥，不进 git）
cp .env.example .env     # 首次；已有 .env 就跳过

# 2. 跑测试
go test ./...

# 3. 编译 + 启动
go build -o bin/ace.exe ./cmd/server
./bin/ace.exe -env .env
```

**默认端口 8090**（8080 被本机另一个程序 `ApplicationWebServer` 占用）。

```bash
curl http://127.0.0.1:8090/healthz
# {"status":"ok","time":"2026-11-02T19:23:17+08:00"}

curl http://127.0.0.1:8090/version
# {"service":"interview-ace","version":"dev"}
```

## 目录结构

```
cmd/server/main.go          入口：加载配置 → 建 logger → 启 HTTP → 优雅关闭
internal/config/            .env 解析 + 校验（环境变量优先于文件）
internal/httpapi/           路由 + 三个中间件（request_id / 访问日志 / panic 兜底）
internal/db/                连接池 + 迁移执行器
internal/db/migrations/     建表 SQL（编译进二进制，不依赖工作目录）
bin/                        编译产物（不进 git）
```

## LLM 调用

```bash
# 真实接口冒烟（花钱、依赖网络，默认跳过）
LLM_SMOKE=1 go test ./internal/llm -run TestChat_真实接口冒烟 -v
```

实测输出：`model=deepseek-flash content="OK" latency=271ms prompt_tokens=9 completion_tokens=1`

**thinking 默认关闭**（`LLM_THINKING=false`）。开着会让 CoT 计入输出 token（价格翻几倍）、
同一输入两次结果差异变大——本项目的评分一致性靠的就是低随机性。

重试策略：只对 429 和 5xx 重试（最多 3 次，0.8s→1.6s 退避）；
401/402/404 和「响应不是 JSON」一律不重试，重试也是一样的结果。
错误信息里不带响应体——401 的 body 有时会回显 key 片段。

## 数据库

**本机 PG 的坑**：沙箱在每次工具调用结束时杀掉整个进程树，`postgres.exe` 活不过调用边界。
`sc.exe` / `schtasks.exe` 都在黑名单、当前 shell 也不是管理员，所以注册服务这条路走不通。

用 `E:\PostgreSQL\with-pg.ps1` 代替：一次调用内启动 → 跑命令 → 干净关停。

```powershell
# 跑数据库集成测试
& 'E:\PostgreSQL\with-pg.ps1' -Command @'
Set-Location 'C:\Users\14814\Desktop\求职项目工作空间\03-InterviewAce-Go'
$env:TEST_DB_DSN = 'postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable'
go test ./internal/db -v
'@
```

迁移设计：

- SQL 文件放 `internal/db/migrations/`，用 `//go:embed` 编译进二进制 → 从任何目录启动都能找到
- 每个文件一个事务，失败整体回滚，不会留下「建了一半的表」
- `schema_migrations` 记录已应用版本，重复执行是幂等的
- 多语句必须走 `QueryExecModeSimpleProtocol`——pgx 默认的扩展协议一次只允许一条语句

## 已完成

| 项 | 状态 | 说明 |
|---|---|---|
| 配置加载 | ✅ | 自写 .env 解析（30 行，不引 godotenv）；必填项缺失一次性报全；报错不含值 |
| 结构化日志 | ✅ | `log/slog` 标准库，不用 zap；只打 `llm_key_set=true/false`，不打密钥 |
| 请求链路追踪 | ✅ | 每个请求一个 `X-Request-ID`，客户端传了就沿用，所有日志行可串起来 |
| 访问日志 | ✅ | method / path / status / latency_ms / ip / request_id |
| panic 兜底 | ✅ | handler 崩了返回 500，堆栈只进日志不给客户端 |
| 健康检查 | ✅ | `/healthz` 存活探针，刻意不查库（查库是 readyz 的活） |
| 就绪探针 | ✅ | `/readyz` 每次真 Ping 库，连不上返回 503；实测杀掉 PG 后立即 503，`healthz` 仍 200 |
| 优雅关闭 | ✅ | 收到信号后给在途请求 10 秒 |
| DB 连接池 | ✅ | pgx v5，MaxConns=10，启动即 Ping（错误在启动时暴露，不是等第一个请求） |
| 迁移执行器 | ✅ | 内嵌 SQL + 事务 + 幂等；服务启动时自动执行，日志打 `migrate_applied versions=[0001_init]` |
| 七张表 | ✅ | users / resumes / interviews / turns / scores / llm_calls / parse_cache |
| 密码哈希 | ✅ | bcrypt cost=10；显式拦 72 字节上限（bcrypt 会静默截断，不拦就等于前 72 字节相同的密码能互相登录） |
| JWT 签发/校验 | ✅ | HS256 + `WithValidMethods` 限定算法；`WithIssuer` + 强制过期 |
| 注册 / 登录接口 | ✅ | `POST /api/auth/register` 201 / 409 / 400；`POST /api/auth/login` 200 / 401 |
| 认证中间件 | ✅ | `RequireAuth` 校验 Bearer 令牌 + **确认用户仍存在**；user_id 放进上下文 |
| user store | ✅ | `Create` / `ByEmail` / `ByID`；邮箱统一小写；23505 翻译成 `ErrEmailTaken` |
| 简历文本处理 | ✅ | 清洗（控制字符/零宽/连字符换行）+ 扫描件判定 + 标题切段 + 降级切片 |
| PDF 文本抽取 | ✅ | `ledongthuc/pdf`；`io.ReaderAt` 随机访问；畸形 PDF 的 panic 兜住转成错误 |
| 简历上传接口 | ✅ | `POST /api/resumes` multipart；401 / 400 / 413 / 422 / 201 |
| 简历列表 / 详情 | ✅ | `GET /api/resumes`、`GET /api/resumes/:id`（越权返回 404，不泄漏存在性） |
| LLM 客户端 | ✅ | 自写 HTTP 客户端（不引 SDK）；429/5xx 指数退避重试；401 不重试；错误信息不带响应体 |
| LLM 结构化解析 | ✅ | 简历正文 → 姓名/教育/项目/工作/技能；失败降级保留原文，状态标 `degraded` |
| 中文 PDF 支持 | ✅ | 实测 CID 字体（STSong-Light + UniGB-UCS2-H）抽取正确，中英混排不丢字 |
| **真实简历实测** | ✅ | 用户真实中文简历：1511 字 → 归并后 1318 字 / 54 行，解析出姓名/学历/1 个项目 9 条要点/7 项技能 |
| Word(.docx) 支持 | ✅ | 标准库 `archive/zip` + `encoding/xml` 解析 `word/document.xml`，不引第三方库；老式 .doc 明确返回 415 |
| 短行归并 | ✅ | 按空行重新拼合被 PDF 拆碎的文本块（239 行 → 54 行），竖线装饰丢弃、括号/冒号/连字符保留 |
| 文件名编码兼容 | ✅ | Windows 客户端按 GBK 发 multipart 文件名，自动解码回 UTF-8 |
| 解析结果校验 | ✅ | 去空条目 / 去重 / 字段截断 300 字 / 要点上限 10 条 / 技能上限 50 条 |
| 解析重试 | ✅ | 非法 JSON 最多重试 3 次，重试时追加「上次不是合法 JSON」的纠正指令；空结果不重试（省钱） |
| 解析缓存 | ✅ | `parse_cache` 表，键 = sha256(模型名 + 正文)；命中后不再调模型；启动时清理 30 天前的 |
| 重复上传去重 | ✅ | `(user_id, content_hash)` 唯一索引；重复返回 200 + `duplicate:true`，不新建记录 |
| 降级可重试 | ✅ | 去重不堵重试：已有 degraded 记录时，新解析成功会覆盖它 |
| 模型用量记账 | ✅ | `llm_calls` 表每次真实调用写一条；`GET /api/stats/llm?days=30` 汇总 |
| store 层集成测试 | ✅ | 43 个用例，真库跑真实 SQL（不再只有假实现） |
| 会话状态机 | ✅ | 6 状态 + 6 事件的转移表；非法流转默认拒绝；CAS 迁移防并发重复开始 |
| 出题 | ✅ | 按简历项目 + JD 出题，输出结构化 JSON；失败重试 3 次 + 纠正指令 |
| 答题 | ✅ | 答案落库（重复提交拒绝）；到轮次上限自动进评分 |
| 追问（七类） | ✅ | 按回答的薄弱点选类型：归因/递进/价值/盲区/验证/边界/权衡；同一话题最多 2 层 |
| 多采样评分 | ✅ | 默认 3 次采样取中位数；分差 > 1 的维度标为「不稳定」；失败次数如实上报 |
| 复盘趋势 | ✅ | `GET /api/stats/trend` + 首页折线图（总分 + 三维度同尺度） |
| 出题用量记账 | ✅ | 每次出题的真实 token 进 `llm_calls`（含重试消耗） |
| 追问类型落库 | ✅ | `turns.followup_kind` 单独一列（迁移 0005）；报告按类型聚合平均分，最弱的排前面 |
| 逐题回顾 | ✅ | 报告页按得分升序列出每题分数与最弱维度；题目从详情接口取，报告不重复存 |
| 评分采样并发 | ✅ | 三次采样并发执行、按采样序号复原；「部分失败」的断言改成与调度无关 |
| 题目相似度去重 | ✅ | 字符二元组 Dice 系数，阈值 0.75；只查换话题的新题，追问不查（天然相似） |
| 错误提示中文化 | ✅ | 13 处只有英文 `error` 没有 `reason` 的返回补齐中文 |
| 回答偏短提示 | ✅ | 少于 15 字标橙并提示「说清怎么做的」，不拦提交 |
| 评分 / 报告 | ✅ | 三维度（准确/清晰/深度）；**总分由程序算**（平均分÷5×100），不信模型给的数字 |
| 前端 | ✅ | React 19 + Vite 6 + Tailwind 3，深色 galaxy 风；构建产物 embed 进二进制 |


## 接口

```
POST /api/auth/register   {"email","password"} → 201 {"user":{"id","email"},"token"}
                          400 invalid_email（含超长）/ invalid_password
                          409 email_taken / 400 invalid_email | invalid_password | invalid_body

POST /api/auth/login      {"email","password"} → 200 {"user","token"}
                          401 invalid_credentials（密码错和账号不存在返回完全相同的响应体）

POST /api/resumes         上传 PDF 或 .docx（multipart，字段名 file）→ 201 {"id","pages","chars","parse_status"}
                          401 未登录 / 400 缺文件 / 413 超过上限
                          415 unsupported_format（.doc 等）
                          422 invalid_document | scanned_pdf
GET  /api/resumes         列表 → {"items":[...],"total":n}
GET  /api/resumes/:id     详情（含 raw_text）；别人的简历返回 404
GET  /api/stats/llm?days=30  模型用量汇总（调用数 / token / 失败数，按用途分组）

POST /api/interviews        {"resume_id","jd_text","role_type","max_turns"} → 201
                            400 invalid_body | jd_too_short | jd_too_long | invalid_role_type
                            404 resume_not_found（别人的简历也是 404）
GET  /api/interviews        列表
GET  /api/interviews/:id    详情（含全部轮次，断点恢复靠它）；越权 404
POST /api/interviews/:id/start   开始 → 生成第一题；重复开始 409
POST /api/interviews/:id/answer  {"answer"} → 下一题 或 finished=true
                            400 answer_too_short | answer_too_long
                            409 invalid_state | already_answered

GET  /healthz             存活探针，不查库
GET  /readyz              就绪探针，查库；连不上 503
GET  /version             版本信息
```

**两个安全细节**：

1. 登录失败不区分「密码错」和「账号不存在」，而且账号不存在时也走一次 bcrypt 比对（`dummyHash`）——否则响应时间差本身就是账号枚举通道
2. 邮箱统一小写存储，避免 `A@x.com` 和 `a@x.com` 变成两个账号

## 追问策略（依据真实面经）

不是随机深挖，是七类固定套路。这些类型来自牛客/CSDN 的面经归纳：

| 类型 | 面经里的真实问法 |
|---|---|
| cause 归因 | 「提升来自哪一类资源」「你觉得本质是什么」 |
| probe 递进 | 做了什么 → 为什么这么设计 → 出过什么问题 → 怎么定位 → 怎么改 |
| value 价值 | 「覆盖率测到之后能拿来干什么」 |
| blindspot 盲区 | 「当初有没有没考虑过的问题」 |
| verify 验证 | 「你怎么知道兜底真的生效了」 |
| boundary 边界 | 「如果并发翻倍呢」 |
| tradeoff 权衡 | 「为什么选 A 不选 B」 |

**同一话题最多追 2 层**（`MaxFollowupDepth`）：模型被要求遵守，程序也硬拦——
超了还返回 followup 就判非法重试。追问链靠 `turns.parent_turn_id` 记录。

## 评分：为什么采样多次

模型对同一份回答的评分本身有抖动，单次评分算出来的「一致性」是假的。

- 默认采样 3 次（`?samples=N` 可调，上限 5）
- 每个维度取**中位数**，不取平均（平均会被一次异常采样拉偏）
- 各次分差 > 1 的维度标为「不稳定」，报告里明说这个分仅供参考
- 失败的采样**如实上报**（`sample_failed`），不静默丢掉

## 前端

**React 19 + Vite 6 + Tailwind 3**，构建产物用 `//go:embed` 嵌进 Go 二进制。

```
web/
├── src/
│   ├── main.jsx            入口
│   ├── App.jsx             四个视图的状态机（不引 react-router）
│   ├── api.js              请求封装（token 注入 / 401 / 网络错误分开报）
│   ├── index.css           Tailwind + 深色基础样式
│   ├── components/
│   │   ├── ui.jsx          Button / Card / Badge / Field / StepBar / ProgressBar
│   │   └── TopBar.jsx
│   └── pages/              Login / Home / Chat / Report
├── dist/                   构建产物（已提交，go:embed 需要）
└── index.legacy.html       改 React 之前的单文件版本，留作对照
```

### 开发（改 UI 不用重编 Go）

```bash
# 终端 1：Go 服务（8090）
./bin/ace.exe -env .env

# 终端 2：Vite 开发服务器（5173，/api 代理到 8090）
cd web && npm run dev
# 打开 http://127.0.0.1:5173
```

改一行 JSX 刷新页面就生效，**不需要 `go build`**。

### 交付（嵌入二进制）

```bash
cd web && npm run build      # 先出 dist
cd .. && go build -o bin/ace.exe ./cmd/server
```

**顺序不能反。** 先 `go build` 的话嵌进去的还是上一次的 dist，
表现为「代码明明改了、页面没变」——这个坑踩过一次。

### 视觉规范

深色 galaxy 风，三条规则写在 `components/ui.jsx` 里：

| 规则 | 为什么 |
|---|---|
| 正文用 `text-slate-300` 不用纯白 | 纯白在深色底上会"发光"，读长句子眼睛疼 |
| 发光只给「要你动手」的地方（按钮 / 聚焦 / 进度条） | 正文区域发光就没法读了 |
| 卡片统一 `.glass`（白 4% 底 + 白 9% 边） | 一处改，全站变 |

### 静态文件与 SPA 兜底

`web/public/` 下的文件（`favicon.svg` 等）会原样复制到 `dist/`。

路由**没有逐个注册**静态文件，而是 `NoRoute` 里判断「dist 里存不存在」：
存在就直接返回文件，不存在才回 `index.html`。

```go
if p := strings.TrimPrefix(c.Request.URL.Path, "/"); p != "" {
    if f, err := distFS.Open(p); err == nil { /* 返回文件 */ }
}
c.Request.URL.Path = "/"  // 其余回 index.html
```

**为什么要这样**：逐个注册的话，每加一个静态文件都要改代码。
之前只注册了 `/favicon.ico`，新加 `favicon.svg` 就漏了，请求落到兜底回了 index.html——
浏览器把 HTML 当图标解析失败，**表现是「图标不显示」但控制台不报错**。

### 图标

- 浏览器标签页：`web/public/favicon.svg`（矢量，任意尺寸清晰）
- exe / 桌面快捷方式：`assets/icon.ico`，由 `tools/genicon` 逐像素画出来

两者用**同一组渐变色**（紫 `#8B5CF6` → 蓝 `#3B82F6` → 青 `#22D3EE`）。
三处图标长得不一样的话，用户会以为是两个不同的东西。

```bash
go run ./tools/genicon assets/icon.ico
~/go/bin/rsrc -ico assets/icon.ico -o cmd/launcher/rsrc_windows_amd64.syso
go build -ldflags "-X main.defaultBase=<项目路径>" -o <桌面>/InterviewAce.exe ./cmd/launcher
```

界面元素参考了 [uiverse-io/galaxy](https://github.com/uiverse-io/galaxy)（3000+ 纯 CSS 元素，MIT）。
只挑了「渐变 / 玻璃 / 光晕」三种手法，没有照搬——社区组件风格不统一，混着用会很花。

## 面试状态机

```
created --start--> asking --submit_answer--> answering --next_question--> asking
                     |                          |
                     +--------finish------------+--> scoring --scored--> done
                     |
                     +--abort--> aborted
```

| 设计 | 为什么 |
|---|---|
| 转移表而不是 if-else | 36 种 (状态, 事件) 组合里只放行 9 种，其余默认拒绝；加状态时不会漏分支 |
| 迁移用 `update ... where status = $from` | 两个请求同时点「开始」，只有一个能把 created 改成 asking。先读再写会两个都成功（实测 8 并发全过） |
| 出题失败回滚到 created | 否则会话卡在 asking 却没有题目，用户没法继续 |
| 答题后停在 answering | 下一题生成失败时答案不丢，可重试 |
| 轮次上限硬边界 | `NextAction` 到上限直接进 scoring，是防失控的第一道闸 |

## 七张表的关键设计

| 设计 | 为什么 |
|---|---|
| `turns` 上 `unique (interview_id, seq)` | 并发重复提交靠数据库兜底，不是应用层「先查再插」（那是 TOCTOU） |
| `turns.parent_turn_id` 自引用 | 追问成树，能回答「这题为什么被追问」 |
| `turns.followup_kind` 单独一列 | 和 `question_type` 正交：一个是话题，一个是挖法；合并成一个字段就没法按类型归因 |
| `scores` 用 `unique nulls not distinct` | 总评那行 `turn_id` 为 null，不加这个 PG 认为每条 null 都不同，允许重复插总评 |
| `interviews` 上部分索引 `where status in ('asking','answering')` | 定时任务只扫进行中的会话，不扫历史 |
| `llm_calls.status` 落库 | 防失控不是嘴上说的，有日志能查 |

## 测试

**346 个用例全绿**，其中 **43 个连真 PostgreSQL 跑**（不是 mock）。

```bash
go test ./...                                                  # 303 个，不需要数据库
TEST_DB_DSN='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable' \
  go test ./...                                                # 346 个，含 43 个真库测试
```

**单元测试 303 个**（不需要数据库）

| 包 | 覆盖 |
|---|---|
| `config` | .env 解析、环境变量优先、必填项一次报全 |
| `httpapi` | 健康检查、request_id 沿用、panic 兜底、readyz 503 |
| `auth` | bcrypt 往返 / 盐随机 / 72 字节边界、JWT 往返 / 换密钥失效 / 过期 / alg=none / 非 HS256 |
| `resume` | 文本清洗、扫描件判定、分段、切片、PDF 抽取（含中文）、短行归并、Word 抽取 |
| `llm` | thinking 开关、Bearer 与 temperature、401 不重试、429 重试到上限、5xx 恢复、上下文取消 |
| `parse` | 解析主路径、重试、清洗校验、边界 |
| `interview` | 状态机 36 种组合、追问七类、深度上限、评分采样与中位数、追问归因、题目去重 |

**集成测试 43 个**（需要 `TEST_DB_DSN`，没设就自动 skip）

每个用例建独立临时库、跑迁移、用完删掉。覆盖：迁移建表与幂等、用户与邮箱归一化、
简历去重与降级覆盖、缓存读写与清理、模型用量记账、面试状态 CAS、轮次唯一约束、
追问类型读写、非法类型被数据库拒绝。

## 六个模块

```
① 面试会话状态机    created → asking → answering → ... → scoring → done
② 追问策略          七类追问（面经归纳）+ 深度上限 2 层 + 相似度去重
③ 评分一致性        三次采样取中位数 + 分差 >1 标不稳定 + 按追问类型归因
④ 防失控三层兜底    轮次上限 / 追问深度上限 / 题目去重
⑤ 简历解析          PDF/Word，失败降级不中断
⑥ 复盘统计          趋势 + 逐题回顾 + 追问归因
```

## 技术栈

```
后端    Go 1.27 / Gin v1.12
数据库  PostgreSQL 16（本机 E:\PostgreSQL，127.0.0.1:5432）
驱动    pgx v5（不用 GORM）
前端    React 19 + Vite + Tailwind
LLM     OpenAI-compatible API
```
