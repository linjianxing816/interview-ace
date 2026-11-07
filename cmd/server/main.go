// InterviewAce 服务入口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"interview-ace/internal/config"
	"interview-ace/internal/db"
	"interview-ace/internal/httpapi"
	"interview-ace/internal/llm"
	"interview-ace/internal/store"
)

// cacheKeep 是解析缓存的保留时长。30 天足够覆盖「同一份简历反复改反复传」的周期，
// 再久的缓存基本不会再命中。
const cacheKeep = 30 * 24 * time.Hour

func main() {
	if err := run(); err != nil {
		// 这里用 fmt 而不是 slog：走到这一步时 logger 可能还没建好
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	envPath := flag.String("env", ".env", "环境变量文件路径")
	flag.Parse()

	cfg, err := config.Load(*envPath)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	// 只打非敏感字段。密钥一个字节都不进日志。
	log.Info("config_loaded",
		"db_host", cfg.DB.Host,
		"db_port", cfg.DB.Port,
		"db_name", cfg.DB.Name,
		"llm_model", cfg.LLM.Model,
		"llm_key_set", cfg.LLM.APIKey != "",
		"server_port", cfg.Server.Port,
	)

	// 连接池在启动时就 Ping，连不上直接失败。
	// 不这么做的话，服务会「启动成功」然后每个请求都 500，排查起来更绕。
	pool, err := db.NewPool(context.Background(), cfg.DB)
	if err != nil {
		return err
	}
	defer pool.Close()

	applied, err := db.Migrate(context.Background(), pool)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		log.Info("migrate_noop", "reason", "schema 已是最新")
	} else {
		log.Info("migrate_applied", "versions", applied)
	}

	// 启动时清理过期缓存。没有常驻调度器，开机清一次是单机部署下够用的做法。
	if removed, err := store.NewParseCache(pool).PurgeOlderThan(context.Background(), cacheKeep); err != nil {
		log.Warn("cache_purge_failed", "error", err)
	} else if removed > 0 {
		log.Info("cache_purged", "removed", removed)
	}

	srv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: httpapi.NewRouter(httpapi.Deps{
			Config:     cfg,
			Log:        log,
			Pool:       pool,
			Users:      store.NewUsers(pool),
			Resumes:    store.NewResumes(pool),
			Parser:     llm.New(cfg.LLM),
			Cache:      store.NewParseCache(pool),
			LLMCalls:   store.NewLLMCalls(pool),
			Interviews: store.NewInterviews(pool),
			Scores:     store.NewScores(pool),
			Trends:     store.NewInterviews(pool),
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("server_start", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("监听 %s 失败: %w", srv.Addr, err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown_signal_received")
	}

	// 给在途请求 10 秒收尾时间，超时强制关闭
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("优雅关闭超时: %w", err)
	}
	log.Info("server_stopped")
	return nil
}
