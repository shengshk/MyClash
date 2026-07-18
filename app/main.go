package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}
	if cfg.ProxyURL != "" {
		u, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			log.Fatalf("TG_PROXY_URL: %v", err)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
	}

	bot, err := tgbotapi.NewBotAPIWithClient(cfg.Token, tgbotapi.APIEndpoint, httpClient)
	if err != nil {
		log.Fatalf("telegram: %v", err)
	}
	bot.Debug = false
	log.Printf("authorized as @%s, whitelist=%v, yaml=%s", bot.Self.UserName, cfg.AllowIDs, cfg.YAMLPath)

	app := &App{
		bot:       bot,
		cfg:       cfg,
		sess:      map[int64]*Session{},
		expireRun: &sync.Map{},
	}

	if len(cfg.Domains) > 0 {
		hub, err := newSubHub(cfg.YAMLPath, cfg.Domains)
		if err != nil {
			log.Fatalf("sub hub: %v", err)
		}
		app.sub = hub
		hub.StartHTTP(cfg.SubListen)
		log.Printf("sub domains=%v links=%d", cfg.Domains, len(hub.Links()))
	} else {
		log.Printf("sub server disabled (no DOMAIN*)")
	}

	app.registerCommands()
	app.resumePendingScrubs()

	bs := app.backups()
	bs.migrateLegacyBaks()
	app.startBackupCron()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.CallbackQuery != nil {
			app.handleCallback(update.CallbackQuery)
			continue
		}
		if update.Message == nil || update.Message.Text == "" {
			continue
		}
		app.handleMessage(update.Message)
	}
}

type Config struct {
	Token      string
	AllowIDs   map[int64]bool
	YAMLPath   string
	ProxyURL   string
	Domains    []string
	SubListen  string
	BackupMax  int
	BackupCron string
}

var tokenRe = regexp.MustCompile(`^(\d+:[A-Za-z0-9_-]+)(?:,(.+))?$`)

func loadConfig() (*Config, error) {
	raw := strings.TrimSpace(os.Getenv("TGBOT"))
	if raw == "" {
		return nil, fmt.Errorf("TGBOT is empty")
	}
	m := tokenRe.FindStringSubmatch(raw)
	if m == nil {
		return nil, fmt.Errorf("TGBOT format: token,id1,id2,...")
	}
	allow := map[int64]bool{}
	if m[2] != "" {
		for _, p := range strings.Split(m[2], ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			id, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid user id %q", p)
			}
			allow[id] = true
		}
	}
	if len(allow) == 0 {
		return nil, fmt.Errorf("TGBOT must include at least one user id")
	}
	path := os.Getenv("YAML_PATH")
	if path == "" {
		path = "/data/aio.yaml"
	}
	listen := strings.TrimSpace(os.Getenv("SUB_LISTEN"))
	if listen == "" {
		listen = subListenDefault
	}
	return &Config{
		Token:      m[1],
		AllowIDs:   allow,
		YAMLPath:   path,
		ProxyURL:   strings.TrimSpace(os.Getenv("TG_PROXY_URL")),
		Domains:    loadDomainsFromEnv(),
		SubListen:  listen,
		BackupMax:  loadBackupMax(),
		BackupCron: strings.TrimSpace(os.Getenv("BACKUP_CRON")),
	}, nil
}
