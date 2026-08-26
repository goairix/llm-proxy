package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goairix/llm-proxy/internal/infrastructure/config"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/database"
	"github.com/goairix/llm-proxy/internal/infrastructure/persistence/migration"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.yaml", "配置文件路径")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "用法: migrate [-config config.yaml] <up|down|status>")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	commands := flags.Args()
	if len(commands) != 1 {
		flags.Usage()
		return 2
	}
	command := commands[0]
	if command != "up" && command != "down" && command != "status" {
		fmt.Fprintf(stderr, "未知迁移命令: %s\n", command)
		flags.Usage()
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "加载配置失败: %v\n", err)
		return 1
	}
	if strings.TrimSpace(cfg.Database.DSN) == "" {
		fmt.Fprintln(stderr, "数据库连接地址未配置，请设置 LLM_PROXY_DATABASE_DSN")
		return 1
	}
	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		fmt.Fprintln(stderr, "数据库连接失败，请检查连接配置和服务状态")
		return 1
	}
	sqlDB, err := db.DB()
	if err != nil {
		fmt.Fprintln(stderr, "获取数据库连接池失败")
		return 1
	}
	defer sqlDB.Close()

	switch command {
	case "up":
		if err := migration.Up(db); err != nil {
			fmt.Fprintf(stderr, "执行数据库迁移失败: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "数据库迁移完成")
	case "down":
		if err := migration.Down(db); err != nil {
			fmt.Fprintf(stderr, "回滚数据库迁移失败: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "数据库迁移回滚完成")
	case "status":
		status, err := migration.Status(db)
		if err != nil {
			fmt.Fprintf(stderr, "查询数据库迁移状态失败: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "当前迁移版本: %s\n", status)
	}
	return 0
}
