package kivo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type KivoBot struct {
	ctx    context.Context
	db     *gorm.DB
	debug  bool
	logger *slog.Logger
}

var ErrDBNotInit = errors.New("database is not initialized")

func (kb *KivoBot) initDB(dbPath string) error {
	logLevel := logger.Silent
	if kb.debug {
		logLevel = logger.Info
	}
	dbConfig := &gorm.Config{
		TranslateError: true,
		Logger: logger.NewSlogLogger(kb.logger, logger.Config{
			LogLevel: logLevel,
		}),
	}
	db, err := gorm.Open(sqlite.Open(dbPath), dbConfig)

	if err != nil {
		return fmt.Errorf("failed to connect database: %w", err)
	}
	if err := db.AutoMigrate(&Member{}, &memberCapability{}); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
	if err := db.AutoMigrate(&Project{}, &projectCapability{}); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
	if err := db.AutoMigrate(&Task{}, &Motion{}); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
	kb.db = db
	return nil
}

func (kb *KivoBot) CreateProject(ctx context.Context, project *Project) error {
	if kb.db == nil {
		return ErrDBNotInit
	}
	return kb.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := gorm.G[Project](tx).Create(ctx, project); err != nil {
			return fmt.Errorf("failed to create project: %w", err)
		}
		var capRows []projectCapability
		for _, cap := range project.RequiredCapabilities {
			capRows = append(capRows, projectCapability{
				ProjectID:  project.UUID,
				Capability: cap,
			})
		}
		if err := gorm.G[projectCapability](tx).CreateInBatches(ctx, &capRows, len(capRows)); err != nil {
			return fmt.Errorf("failed to create project capability: %w", err)
		}
		return nil
	})
}

func (kb *KivoBot) GetProject(ctx context.Context, projectID string) (Project, error) {
	if kb.db == nil {
		return Project{}, ErrDBNotInit
	}
	if projectID == "" {
		return Project{}, errors.New("projectID cannot be empty")
	}
	capRows, err := gorm.G[projectCapability](kb.db).Preload("Project", nil).Where("project_id = ?", projectID).Find(ctx)
	if err != nil {
		return Project{}, fmt.Errorf("failed to find project capabilities: %w", err)
	}
	var project *Project
	if len(capRows) > 0 {
		project = &capRows[0].Project
		for _, row := range capRows {
			project.RequiredCapabilities = append(project.RequiredCapabilities, row.Capability)
		}
	} else {
		projectVal, err := gorm.G[Project](kb.db).Where("uuid = ?", projectID).First(ctx)
		if err != nil {
			return Project{}, fmt.Errorf("failed to find project: %w", err)
		}
		project = &projectVal
	}
	return *project, nil
}

func (kb *KivoBot) ProjectCounts(ctx context.Context) (int64, error) {
	if kb.db == nil {
		return 0, ErrDBNotInit
	}
	count, err := gorm.G[Project](kb.db).Count(ctx, "*")
	if err != nil {
		return 0, fmt.Errorf("failed to count projects: %w", err)
	}
	return count, nil
}

func (kb *KivoBot) QueryProjects(ctx context.Context, name string) ([]Project, error) {
	// 主要就是当GetAllProjects用
	if kb.db == nil {
		return nil, ErrDBNotInit
	}
	if name == "" {
		return gorm.G[Project](kb.db).Find(ctx)
	}
	return gorm.G[Project](kb.db).Where("name = ?", name).Find(ctx)
}

func (kb *KivoBot) QueryMembersByName(ctx context.Context, name string) (Member, error) {
	if kb.db == nil {
		return Member{}, ErrDBNotInit
	}
	return gorm.G[Member](kb.db).Where("name = ?", name).First(ctx)
}

func (kb *KivoBot) CreateMember(ctx context.Context, member *Member) error {
	if kb.db == nil {
		return ErrDBNotInit
	}
	return kb.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := gorm.G[Member](tx).Create(ctx, member); err != nil {
			return fmt.Errorf("failed to create member: %w", err)
		}
		var capRows []memberCapability
		for _, cap := range member.Capabilities {
			capRows = append(capRows, memberCapability{
				MemberID:   member.ID,
				Capability: cap,
			})
		}
		if err := gorm.G[memberCapability](tx).CreateInBatches(ctx, &capRows, len(capRows)); err != nil {
			return fmt.Errorf("failed to create member capability: %w", err)
		}
		return nil
	})
}

func (kb *KivoBot) MemberCounts(ctx context.Context) (int64, error) {
	if kb.db == nil {
		return 0, ErrDBNotInit
	}
	count, err := gorm.G[Member](kb.db).Count(ctx, "*")
	if err != nil {
		return 0, fmt.Errorf("failed to count members: %w", err)
	}
	return count, nil
}

func (kb *KivoBot) EnableMember(ctx context.Context, memberID string) error {
	if kb.db == nil {
		return ErrDBNotInit
	}
	if memberID == "" {
		return errors.New("memberID cannot be empty")
	}
	if _, err := gorm.G[Member](kb.db).Where("id = ?", memberID).Update(ctx, "available", true); err != nil {
		return fmt.Errorf("failed to enable member: %w", err)
	}
	return nil
}

func (kb *KivoBot) DisableMember(ctx context.Context, memberID string) error {
	if kb.db == nil {
		return ErrDBNotInit
	}
	if memberID == "" {
		return errors.New("memberID cannot be empty")
	}
	if _, err := gorm.G[Member](kb.db).Where("id = ?", memberID).Update(ctx, "available", false); err != nil {
		return fmt.Errorf("failed to disable member: %w", err)
	}
	return nil
}

func (kb *KivoBot) GetMember(ctx context.Context, memberID string) (Member, error) {
	if kb.db == nil {
		return Member{}, ErrDBNotInit
	}
	capRows, err := gorm.G[memberCapability](kb.db).Preload("Member", nil).Where("member_id = ?", memberID).Find(ctx)
	if err != nil {
		return Member{}, fmt.Errorf("failed to find member capabilities: %w", err)
	}
	var member *Member
	if len(capRows) > 0 {
		member = &capRows[0].Member
		for _, row := range capRows {
			member.Capabilities = append(member.Capabilities, row.Capability)
		}
	} else {
		memberVal, err := gorm.G[Member](kb.db).Where("id = ?", memberID).First(ctx)
		if err != nil {
			return Member{}, fmt.Errorf("failed to find member: %w", err)
		}
		member = &memberVal
	}

	return *member, nil
}

func (kb *KivoBot) QueryMembersByCap(ctx context.Context, caps []Capability) ([]Member, error) {
	if kb.db == nil {
		return nil, ErrDBNotInit
	}

	query := kb.db.Model(&memberCapability{}).Select("member_id").Where("capability IN ?", caps)
	rows, err := gorm.G[memberCapability](kb.db).Preload("Member", nil).Where("member_id IN (?)", query).Find(ctx)

	if err != nil {
		return nil, fmt.Errorf("failed to find member capabilities: %w", err)
	}
	members := make([]Member, 0)
	memberIndex := make(map[string]int)

	for _, row := range rows {
		i, ok := memberIndex[row.MemberID]
		if !ok {
			i = len(members)
			memberIndex[row.MemberID] = i
			members = append(members, row.Member)
		}

		members[i].Capabilities = append(
			members[i].Capabilities,
			row.Capability,
		)
	}

	return members, nil
}

func (kb *KivoBot) AddCapability(ctx context.Context, memberID string, cap Capability) error {
	if kb.db == nil {
		return ErrDBNotInit
	}
	if memberID == "" {
		return errors.New("memberID cannot be empty")
	}
	if cap == "" {
		return errors.New("capability cannot be empty")
	}
	if err := gorm.G[memberCapability](kb.db).Create(ctx, &memberCapability{MemberID: memberID, Capability: cap}); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil
		}
		return fmt.Errorf("failed to add capability: %w", err)
	}
	return nil
}

func (kb *KivoBot) RemoveCapability(ctx context.Context, memberID string, cap Capability) error {
	if kb.db == nil {
		return ErrDBNotInit
	}
	if memberID == "" {
		return errors.New("memberID cannot be empty")
	}
	if cap == "" {
		return errors.New("capability cannot be empty")
	}
	if _, err := gorm.G[memberCapability](kb.db).Where(memberCapability{MemberID: memberID, Capability: cap}).Delete(ctx); err != nil {
		return fmt.Errorf("failed to remove capability: %w", err)
	}
	return nil
}

func NewKivoBot(ctx context.Context, config *BotConfig) (*KivoBot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if config == nil {
		return nil, errors.New("config cannot be nil")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	bot := &KivoBot{
		ctx:    ctx,
		debug:  config.Debug,
		logger: config.Logger,
	}
	if err := bot.initDB(config.DBPath); err != nil {
		return nil, fmt.Errorf("failed to initialize KivoBot: %w", err)
	}
	return bot, nil
}
