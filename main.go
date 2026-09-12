package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/pterm/pterm"
	"github.com/q1bksuu/onebot-go-sdk/v11/application"
	"github.com/q1bksuu/onebot-go-sdk/v11/entity"
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
	decoder := entity.NewEventDecoder()
	var ws *application.WebSocketClient
	ws, err = application.NewWebSocketClient(application.WebSocketConfig{
		URL:         os.Getenv("ONEBOT_WS_URL"),
		AccessToken: os.Getenv("ONEBOT_ACCESS_TOKEN"),
		DecodeEvent: decoder.DecodeEvent,
		OnEvent: func(ctx context.Context, event entity.Event) error {
			switch event := event.(type) {
			case *entity.GroupMessageEvent:
				slog.Info("gm", "group", event.GroupId, "message_id", event.MessageId, "qq", event.Sender.Nickname)
			case *entity.PrivateMessageEvent:
				slog.Info("pm", "user_id", event.Sender.UserId, "message_id", event.MessageId)
				_, err = ws.SendPrivateMsg(ctx, &entity.SendPrivateMsgRequest{UserId: event.Sender.UserId, Message: &entity.MessageValue{Type: entity.MessageValueTypeString, StringValue: "test"}})
				if err != nil {
					slog.Warn("send err", "err", err)
				}
			case *entity.RawEvent:
				slog.Info("unknown event", "evt", event.Raw)
			}
			return nil
		},
		OnError: func(err error) { slog.Info("OneBot", "err", err) },
	})
	if err != nil {
		panic(err)
	}
	runErr := make(chan error)
	go func() {
		if err := ws.Run(ctx); err != nil {
			runErr <- err
		}
	}()
	if err := ws.WaitReady(ctx); err != nil {
		panic(err)
	}

	slog.Info("Bot ready")

	defer ws.Close()

	for {
		select {
		case err := <-runErr:
			slog.Warn("ws run error", "err", err)
		case <-ctx.Done():
			slog.Info("Context done")
			return
		}
	}
}
