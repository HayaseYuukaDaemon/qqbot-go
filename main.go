package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"qqbot/hitomi"
	"strconv"
	"strings"
	"time"

	"github.com/pterm/pterm"
	napcat "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

func parseLogLevel(s string) (pterm.LogLevel, error) {
	if s == "" {
		return pterm.LogLevelInfo, nil
	}
	switch s {
	case "DEBUG":
		return pterm.LogLevelDebug, nil
	case "INFO":
		return pterm.LogLevelInfo, nil
	case "WARN":
		return pterm.LogLevelWarn, nil
	}
	return 0, fmt.Errorf("Invalid level")
}

func main() {
	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		panic(err)
	}
	logOpt := pterm.DefaultLogger
	logOpt.Level = level
	slog.SetDefault(slog.New(pterm.NewSlogHandler(&logOpt)))

	slog.Info("Start")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer func() {
		slog.Info("Stopping context")
		stop()
	}()
	client, err := napcat.DialWebSocket(
		ctx,
		"ws://127.0.0.1:3000",
		napcat.WithToken(os.Getenv("NAPCAT_TOKEN")),
		napcat.WithEventBuffer(1024),
		napcat.WithEventDeliveryTimeout(time.Second),
	)
	if err != nil {
		panic(err)
	}
	defer func() {
		slog.Info("Closing client")
		client.Close()
	}()
	resp, err := client.API().GetLoginInfo(ctx, nil)
	if err != nil {
		panic(err)
	}
	slog.Info("Login info", "info", resp.Nickname)
	hitomiClient := hitomi.NewHitomiClient(nil, 5)
	for {
		select {
		case ev := <-client.Events():
			switch ev := ev.(type) {
			case *event.GroupMessage:
				userID := ev.Sender.UserID
				groupID := string(ev.GroupID)
				slog.Debug("Group message", "type", string(ev.PostType()), "gid", groupID, "sender", ev.Sender.Nickname, "message", ev.Message.Text())
				msgText := ev.Message.Text()
				hitomiIDStr, found := strings.CutPrefix(msgText, "/h ")
				if !found {
					continue
				}

				if err != nil {
					slog.Warn("failed to construct msg", "err", err)
					continue
				}
				hitomiID, err := strconv.Atoi(hitomiIDStr)
				if err != nil {
					replyMsg, err := api.NewOB11Message([]message.Segment{message.Reply(userID.Int64()), message.Text("Invalid hitomi ID")})
					if err != nil {
						slog.Warn("failed to construct reply msg", "err", err)
						continue
					}
					_, err = client.API().SendGroupMsg(ctx, api.SendGroupMsgRequest{
						GroupID: &groupID,
						Message: replyMsg,
					})

					if err != nil {
						slog.Warn("failed to send reply msg", "err", err)
					}
					continue
				}
				gallery, err := hitomiClient.SearchIDs(ctx, hitomiID)
				if err != nil {
					slog.Warn("failed to search hitomi IDs", "err", err)
					continue
				}
				// Process the search results (e.g., send them back to the user)
			}
		case <-ctx.Done():
			slog.Info("Context done")
			return
		}
	}
}
