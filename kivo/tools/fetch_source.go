package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"qqbot/kivo"

	"gorm.io/gorm"
)

var GROUP_MAP = map[string]kivo.Capability{
	"form_filling": kivo.FormFilling,
	"bbq":          kivo.Translator,
}

func main() {
	MEMBER_JSON_URL := os.Getenv("MEMBER_JSON_URL")
	if MEMBER_JSON_URL == "" {
		panic("MEMBER_JSON_URL is not set")
	}
	configFile, err := os.OpenFile("config.json", os.O_RDONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer configFile.Close()
	var config kivo.BotConfig
	if err := json.NewDecoder(configFile).Decode(&config); err != nil {
		panic(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer func() {
		stop()
	}()

	req, err := http.NewRequestWithContext(ctx, "GET", MEMBER_JSON_URL, nil)
	if err != nil {
		panic(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		panic("failed to fetch member json")
	}
	var SourceJSON struct {
		Groups []struct {
			ID      string `json:"id"`
			Members []struct {
				Name     string `json:"name"`
				Excluded bool   `json:"excluded"`
			} `json:"members"`
		} `json:"groups"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&SourceJSON); err != nil {
		panic(err)
	}
	resp.Body.Close()
	kb, err := kivo.NewKivoBot(ctx, &kivo.BotConfig{
		DBPath: config.DBPath,
	})
	if err != nil {
		panic(err)
	}
	for _, group := range SourceJSON.Groups {
		capability, ok := GROUP_MAP[group.ID]
		if !ok {
			continue
		}
		slog.Info("处理Group", "group_id", group.ID, "capability", capability)
		for _, m := range group.Members {
			member, err := kb.QueryMembersByName(ctx, m.Name)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					slog.Warn("成员不存在, 跳过", "name", m.Name)
					continue
				} else {
					panic(err)
				}
			}
			slog.Info("为成员添加能力", "name", m.Name, "cap", capability)
			if err := kb.AddCapability(ctx, member.ID, capability); err != nil {
				panic(err)
			}
			if m.Excluded {
				slog.Info("排除成员", "name", m.Name)
				if err := kb.DisableMember(ctx, member.ID); err != nil {
					panic(err)
				}
			}
		}
	}
}
