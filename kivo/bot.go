package kivo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type KivoBot struct {
	ctx context.Context
	db  *gorm.DB
}

func (kb *KivoBot) initDB(dbPath string) error {
	uuid.New()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}
	kb.db = db
	return nil
}

func NewKivoBot(ctx context.Context) (*KivoBot, error) {
	return nil, nil
}
