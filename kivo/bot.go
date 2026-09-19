package kivo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/mattn/go-sqlite3"
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
		return fmt.Errorf("failed to open db: %w", err)
	}
	if err := db.Exec("PRAGMA foreign_keys = ON;").Error; err != nil {
		return fmt.Errorf("failed to enable foreign key: %w", err)
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

	fmt.Println(
		db.Migrator().HasConstraint(&Task{}, "AllocatedMember"),
	)

	if err := db.Exec(`
		CREATE TRIGGER IF NOT EXISTS task_member_available_on_insert
		BEFORE INSERT ON tasks
		WHEN NEW.allocated_member_id IS NOT NULL
		 AND NOT EXISTS (
			SELECT 1
			FROM members
			WHERE id = NEW.allocated_member_id
			  AND available = 1
		 )
		BEGIN
			SELECT RAISE(ABORT, 'allocated member is not available');
		END;
	`).Error; err != nil {
		return fmt.Errorf("failed to create db trigger task_member_available_on_insert: %w", err)
	}

	if err := db.Exec(`
		CREATE TRIGGER IF NOT EXISTS task_member_available_on_update
		BEFORE UPDATE OF allocated_member_id ON tasks
		WHEN NEW.allocated_member_id IS NOT NULL
		 AND NOT EXISTS (
			SELECT 1
			FROM members
			WHERE id = NEW.allocated_member_id
			  AND available = 1
		 )
		BEGIN
			SELECT RAISE(ABORT, 'allocated member is not available');
		END;
	`).Error; err != nil {
		return fmt.Errorf("failed to create db trigger task_member_available_on_update: %w", err)
	}

	// 状态流转需要同时检查更新前后的值，由触发器在数据库层约束。
	if err := db.Exec(`
		CREATE TRIGGER IF NOT EXISTS task_accepted_transition_on_update
		BEFORE UPDATE OF status ON tasks
		WHEN NEW.status = 'accepted'
		 AND OLD.status NOT IN ('allocated', 'pending', 'blocked')
		BEGIN
			SELECT RAISE(ABORT, 'task can only transition to accepted from allocated, pending or blocked');
		END;
	`).Error; err != nil {
		return fmt.Errorf("failed to create db trigger task_accepted_transition_on_update: %w", err)
	}

	kb.db = db
	return nil
}

func (kb *KivoBot) AcceptTask(ctx context.Context, taskID string) error {
	rows, err := gorm.G[Task](kb.db).Where("id = ?", taskID).Update(ctx, "status", TaskAccepted)
	if err != nil {
		return fmt.Errorf("failed to accept task: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("task not found")
	}
	return nil
}

type QueryTaskParams struct {
	ProjectID         *string
	AllocatedMemberID *string
}

// QueryTasks 对所有非 nil 条件取交集，ID 精确匹配，包括非 nil 的空字符串。
// 返回任务及其项目、已分配成员的完整能力；未分配任务的成员保持 nil。
func (kb *KivoBot) QueryTasks(ctx context.Context, params QueryTaskParams) ([]Task, error) {
	if kb.db == nil {
		return nil, ErrDBNotInit
	}
	conditions := make(map[string]any)
	if params.ProjectID != nil {
		conditions["project_id"] = *params.ProjectID
	}
	if params.AllocatedMemberID != nil {
		conditions["allocated_member_id"] = *params.AllocatedMemberID
	}
	query := gorm.G[Task](kb.db).Where(conditions)
	tasks, err := query.Preload("Project", nil).Preload("AllocatedMember", nil).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find tasks: %w", err)
	}
	if len(tasks) == 0 {
		return tasks, nil
	}

	// 批量补齐 gorm:"-" 的能力字段，避免每个任务分别查询关联对象。
	projectCaps := make(map[string][]Capability)
	memberCaps := make(map[string][]Capability)
	var projectIDs, memberIDs []string
	for _, task := range tasks {
		if _, ok := projectCaps[task.ProjectID]; !ok {
			projectIDs = append(projectIDs, task.ProjectID)
			projectCaps[task.ProjectID] = nil
		}
		if task.AllocatedMemberID != nil {
			id := *task.AllocatedMemberID
			if _, ok := memberCaps[id]; !ok {
				memberIDs = append(memberIDs, id)
				memberCaps[id] = nil
			}
		}
	}
	projectRows, err := gorm.G[projectCapability](kb.db).Where("project_id IN ?", projectIDs).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find task project capabilities: %w", err)
	}
	for _, row := range projectRows {
		projectCaps[row.ProjectID] = append(projectCaps[row.ProjectID], row.Capability)
	}
	if len(memberIDs) > 0 {
		memberRows, err := gorm.G[memberCapability](kb.db).Where("member_id IN ?", memberIDs).Find(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to find task member capabilities: %w", err)
		}
		for _, row := range memberRows {
			memberCaps[row.MemberID] = append(memberCaps[row.MemberID], row.Capability)
		}
	}
	for i := range tasks {
		tasks[i].Project.RequiredCapabilities = projectCaps[tasks[i].ProjectID]
		if member := tasks[i].AllocatedMember; member != nil {
			member.Capabilities = memberCaps[member.ID]
		}
	}
	return tasks, nil
}

func (kb *KivoBot) CreateTask(ctx context.Context, task *Task) error {
	if kb.db == nil {
		return ErrDBNotInit
	}
	err := gorm.G[Task](kb.db).Create(ctx, task)
	if err == nil {
		return nil
	}
	if sqliteErr, ok := errors.AsType[sqlite3.Error](err); ok {
		switch sqliteErr.ExtendedCode {
		case sqlite3.ErrConstraintCheck:
			return fmt.Errorf("failed to create task: check constraint violated: %w", err)
		case sqlite3.ErrConstraintTrigger:
			var memberID *string
			if task.AllocatedMemberID != nil {
				memberID = task.AllocatedMemberID
			} else {
				memberID = &task.AllocatedMember.ID
			}
			return fmt.Errorf("%w: member: %s", err, *memberID)
		}
	}
	return err
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

type QueryMemberParams struct {
	Name         *string
	Capabilities *[]Capability
	Available    *bool
}

// QueryMembers 对所有非 nil 条件取交集，Name 精确匹配，Capabilities 要求全部具备。
// nil 或空能力列表不限制能力；返回匹配成员的全部能力，包括没有能力的成员。
func (kb *KivoBot) QueryMembers(ctx context.Context, params QueryMemberParams) ([]Member, error) {
	if kb.db == nil {
		return nil, ErrDBNotInit
	}

	conditions := make(map[string]any)
	if params.Name != nil {
		conditions["name"] = *params.Name
	}
	if params.Available != nil {
		conditions["available"] = *params.Available
	}
	query := gorm.G[Member](kb.db).Where(conditions)
	if params.Capabilities != nil && len(*params.Capabilities) > 0 {
		// 能力按集合匹配，重复的查询条件不增加所需能力数量。
		seen := make(map[Capability]struct{})
		caps := make([]Capability, 0, len(*params.Capabilities))
		for _, cap := range *params.Capabilities {
			if _, ok := seen[cap]; !ok {
				seen[cap] = struct{}{}
				caps = append(caps, cap)
			}
		}
		subQuery := kb.db.Model(&memberCapability{}).
			Select("member_id").Where("capability IN ?", caps).
			Group("member_id").Having("COUNT(DISTINCT capability) = ?", len(caps))
		query = query.Where("id IN (?)", subQuery)
	}
	members, err := query.Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find members: %w", err)
	}
	if len(members) == 0 {
		return members, nil
	}

	memberIDs := make([]string, 0, len(members))
	memberIndex := make(map[string]int, len(members))
	for i, member := range members {
		memberIDs = append(memberIDs, member.ID)
		memberIndex[member.ID] = i
	}
	rows, err := gorm.G[memberCapability](kb.db).Where("member_id IN ?", memberIDs).Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find member capabilities: %w", err)
	}
	for _, row := range rows {
		i := memberIndex[row.MemberID]
		members[i].Capabilities = append(members[i].Capabilities, row.Capability)
	}

	return members, nil
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
