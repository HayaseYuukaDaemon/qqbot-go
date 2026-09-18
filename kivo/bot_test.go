package kivo

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

// 每个测试使用独立的内存数据库；固定单连接，避免 SQLite 为新连接创建空库。
func newTestBot(t *testing.T) *KivoBot {
	t.Helper()
	bot, err := NewKivoBot(t.Context(), &BotConfig{DBPath: ":memory:"})
	if err != nil {
		t.Fatalf("NewKivoBot: %v", err)
	}
	db, err := bot.db.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return bot
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// 能力是集合，查询结果的顺序不属于接口约定，同时保留对重复能力的检查。
func requireCapabilities(t *testing.T, got, want []Capability) {
	t.Helper()
	got, want = slices.Clone(got), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
}

func TestBotMemberCapabilities(t *testing.T) {
	ctx := t.Context()
	bot := newTestBot(t)
	yuuka := Member{Name: "HayaseYuuka", ID: "123", Available: true, Capabilities: []Capability{FormFilling}}
	hina := Member{Name: "SorasakiHina", ID: "456", Available: false, Capabilities: []Capability{Administrator}}
	requireNoError(t, bot.CreateMember(ctx, &yuuka))
	requireNoError(t, bot.CreateMember(ctx, &hina))

	for _, want := range []Member{yuuka, hina} {
		got, err := bot.GetMember(ctx, want.ID)
		requireNoError(t, err)
		if got.ID != want.ID || got.Name != want.Name || got.Available != want.Available {
			t.Fatalf("GetMember(%q) = %+v, want %+v", want.ID, got, want)
		}
		requireCapabilities(t, got.Capabilities, want.Capabilities)
	}

	requireNoError(t, bot.AddCapability(ctx, yuuka.ID, Assistant))
	got, err := bot.GetMember(ctx, yuuka.ID)
	requireNoError(t, err)
	requireCapabilities(t, got.Capabilities, []Capability{FormFilling, Assistant})

	requireNoError(t, bot.RemoveCapability(ctx, yuuka.ID, FormFilling))
	// 重复添加不报错、不产生重复记录；删除已经不存在的能力也成功。
	requireNoError(t, bot.AddCapability(ctx, yuuka.ID, Assistant))
	requireNoError(t, bot.RemoveCapability(ctx, yuuka.ID, FormFilling))
	got, err = bot.GetMember(ctx, yuuka.ID)
	requireNoError(t, err)
	requireCapabilities(t, got.Capabilities, []Capability{Assistant})

	// 删除最后一个能力后，成员仍然可以查询。
	requireNoError(t, bot.RemoveCapability(ctx, yuuka.ID, Assistant))
	got, err = bot.GetMember(ctx, yuuka.ID)
	requireNoError(t, err)
	if got.ID != yuuka.ID || got.Name != yuuka.Name || !got.Available {
		t.Fatalf("member without capabilities = %+v", got)
	}
	requireCapabilities(t, got.Capabilities, nil)
	got, err = bot.GetMember(ctx, hina.ID)
	requireNoError(t, err)
	requireCapabilities(t, got.Capabilities, []Capability{Administrator})
}

func TestBotProjectCapabilitiesAreIndependentOfMembers(t *testing.T) {
	ctx := t.Context()
	bot := newTestBot(t)
	member := Member{ID: "123", Name: "HayaseYuuka", Available: true, Capabilities: []Capability{FormFilling}}
	project := Project{UUID: "test-project", Name: "test", Desc: "表单项目", RequiredCapabilities: []Capability{FormFilling, Assistant}}
	requireNoError(t, bot.CreateMember(ctx, &member))
	requireNoError(t, bot.CreateProject(ctx, &project))

	checkProject := func() {
		t.Helper()
		got, err := bot.GetProject(ctx, project.UUID)
		requireNoError(t, err)
		if got.UUID != project.UUID || got.Name != project.Name || got.Desc != project.Desc {
			t.Fatalf("GetProject = %+v, want %+v", got, project)
		}
		requireCapabilities(t, got.RequiredCapabilities, project.RequiredCapabilities)
	}
	checkProject()
	requireNoError(t, bot.AddCapability(ctx, member.ID, Assistant))
	requireNoError(t, bot.RemoveCapability(ctx, member.ID, FormFilling))
	checkProject()
}

func TestBotQueryMembers(t *testing.T) {
	ctx := t.Context()
	bot := newTestBot(t)
	yuuka := Member{ID: "123", Name: "HayaseYuuka", Available: true, Capabilities: []Capability{FormFilling, Assistant}}
	hina := Member{ID: "456", Name: "SorasakiHina", Available: false, Capabilities: []Capability{Administrator}}
	aris := Member{ID: "789", Name: "TendouAris", Available: false, Capabilities: []Capability{FormFilling, Assistant, Administrator}}
	noCaps := Member{ID: "000", Name: "", Available: true, Capabilities: []Capability{Translator}}
	for _, member := range []*Member{&yuuka, &hina, &aris, &noCaps} {
		requireNoError(t, bot.CreateMember(ctx, member))
	}
	requireNoError(t, bot.RemoveCapability(ctx, noCaps.ID, Translator))
	noCaps.Capabilities = nil
	all := []Member{yuuka, hina, aris, noCaps}
	caps := func(values ...Capability) *[]Capability { return &values }
	var nilCaps []Capability
	emptyCaps := []Capability{}
	partialName := "Hayase"
	missingName := "' OR 1=1 --"

	// 非 nil 字段全部匹配，能力是包含关系，结果保留成员的全部能力。
	for _, tt := range []struct {
		name   string
		params QueryMemberParams
		want   []Member
	}{
		{"no filters includes members without capabilities", QueryMemberParams{}, all},
		{"pointer to nil capabilities does not filter", QueryMemberParams{Capabilities: &nilCaps}, all},
		{"pointer to empty capabilities does not filter", QueryMemberParams{Capabilities: &emptyCaps}, all},
		{"matching member includes all capabilities", QueryMemberParams{Capabilities: caps(FormFilling)}, []Member{yuuka, aris}},
		{"unavailable member is included by default", QueryMemberParams{Capabilities: caps(Administrator)}, []Member{hina, aris}},
		{"must match all capabilities", QueryMemberParams{Capabilities: caps(FormFilling, Administrator)}, []Member{aris}},
		{"multiple matches return each member once", QueryMemberParams{Capabilities: caps(FormFilling, Assistant)}, []Member{yuuka, aris}},
		{"duplicate query capability", QueryMemberParams{Capabilities: caps(Assistant, Assistant)}, []Member{yuuka, aris}},
		{"duplicates still require other capabilities", QueryMemberParams{Capabilities: caps(Assistant, Assistant, Administrator)}, []Member{aris}},
		{"no matching capability", QueryMemberParams{Capabilities: caps(Translator)}, nil},
		{"one missing capability rejects member", QueryMemberParams{Capabilities: caps(FormFilling, Translator)}, nil},
		{"empty capability is a condition", QueryMemberParams{Capabilities: caps("")}, nil},
		{"name filter", QueryMemberParams{Name: &yuuka.Name}, []Member{yuuka}},
		{"name is exact", QueryMemberParams{Name: &partialName}, nil},
		{"name is bound as a value", QueryMemberParams{Name: &missingName}, nil},
		{"empty name is a condition", QueryMemberParams{Name: &noCaps.Name}, []Member{noCaps}},
		{"available true", QueryMemberParams{Available: &yuuka.Available}, []Member{yuuka, noCaps}},
		{"available false is a condition", QueryMemberParams{Available: &hina.Available}, []Member{hina, aris}},
		{"capabilities and available true", QueryMemberParams{Capabilities: caps(FormFilling, Assistant), Available: &yuuka.Available}, []Member{yuuka}},
		{"capabilities and available false", QueryMemberParams{Capabilities: caps(FormFilling, Assistant), Available: &hina.Available}, []Member{aris}},
		{"empty capabilities still filter availability", QueryMemberParams{Capabilities: &emptyCaps, Available: &hina.Available}, []Member{hina, aris}},
		{"all fields match", QueryMemberParams{Name: &aris.Name, Capabilities: caps(FormFilling, Administrator), Available: &aris.Available}, []Member{aris}},
		{"name conflicts with capabilities", QueryMemberParams{Name: &yuuka.Name, Capabilities: caps(Administrator)}, nil},
		{"name conflicts with availability", QueryMemberParams{Name: &yuuka.Name, Available: &hina.Available}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bot.QueryMembers(ctx, tt.params)
			requireNoError(t, err)
			if len(got) != len(tt.want) {
				t.Fatalf("QueryMembers(%+v) = %+v, want %+v", tt.params, got, tt.want)
			}
			wantByID := make(map[string]Member, len(tt.want))
			for _, member := range tt.want {
				wantByID[member.ID] = member
			}
			for _, member := range got {
				want, ok := wantByID[member.ID]
				if !ok {
					t.Fatalf("unexpected or duplicate member: %+v", member)
				}
				if member.Name != want.Name || member.Available != want.Available {
					t.Fatalf("member = %+v, want %+v", member, want)
				}
				requireCapabilities(t, member.Capabilities, want.Capabilities)
				delete(wantByID, member.ID)
			}
		})
	}
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := bot.QueryMembers(ctx, QueryMemberParams{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("QueryMembers = %v, want context cancelled", err)
		}
	})
	t.Run("database not initialized", func(t *testing.T) {
		var bot KivoBot
		if _, err := bot.QueryMembers(t.Context(), QueryMemberParams{}); !errors.Is(err, ErrDBNotInit) {
			t.Fatalf("QueryMembers = %v, want ErrDBNotInit", err)
		}
	})
}

func TestBotCapabilityValidation(t *testing.T) {
	bot := newTestBot(t)
	ctx := t.Context()
	member := Member{ID: "123", Name: "HayaseYuuka", Capabilities: []Capability{FormFilling}}
	requireNoError(t, bot.CreateMember(ctx, &member))
	for _, operation := range []struct {
		name string
		run  func(context.Context, string, Capability) error
	}{
		{"add", bot.AddCapability},
		{"remove", bot.RemoveCapability},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, input := range []struct {
				name string
				id   string
				cap  Capability
			}{
				{"empty member ID", "", FormFilling},
				{"empty capability", member.ID, ""},
			} {
				t.Run(input.name, func(t *testing.T) {
					if err := operation.run(ctx, input.id, input.cap); err == nil {
						t.Fatal("expected validation error")
					}
				})
			}
		})
	}
	if err := bot.AddCapability(ctx, "missing", Assistant); !errors.Is(err, gorm.ErrForeignKeyViolated) {
		t.Fatalf("add capability to missing member: %v, want foreign key violation", err)
	}
	got, err := bot.GetMember(ctx, member.ID)
	requireNoError(t, err)
	requireCapabilities(t, got.Capabilities, member.Capabilities)
}

func TestBotCreateRollsBackOnDuplicateCapability(t *testing.T) {
	// 主记录和能力记录在同一事务中，能力写入失败不能留下主记录。
	t.Run("member", func(t *testing.T) {
		bot := newTestBot(t)
		ctx := t.Context()
		member := Member{ID: "123", Name: "HayaseYuuka", Capabilities: []Capability{Assistant, Assistant}}
		if err := bot.CreateMember(ctx, &member); !errors.Is(err, gorm.ErrDuplicatedKey) {
			t.Fatalf("CreateMember = %v, want duplicate key", err)
		}
		if _, err := bot.GetMember(ctx, member.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("GetMember after rollback = %v, want record not found", err)
		}
		member.Capabilities = []Capability{Assistant}
		requireNoError(t, bot.CreateMember(ctx, &member))
		got, err := bot.GetMember(ctx, member.ID)
		requireNoError(t, err)
		requireCapabilities(t, got.Capabilities, member.Capabilities)
	})
	t.Run("project", func(t *testing.T) {
		bot := newTestBot(t)
		ctx := t.Context()
		project := Project{UUID: "test-project", Name: "test", RequiredCapabilities: []Capability{FormFilling, FormFilling}}
		if err := bot.CreateProject(ctx, &project); !errors.Is(err, gorm.ErrDuplicatedKey) {
			t.Fatalf("CreateProject = %v, want duplicate key", err)
		}
		if _, err := bot.GetProject(ctx, project.UUID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("GetProject after rollback = %v, want record not found", err)
		}
		project.RequiredCapabilities = []Capability{FormFilling}
		requireNoError(t, bot.CreateProject(ctx, &project))
		got, err := bot.GetProject(ctx, project.UUID)
		requireNoError(t, err)
		requireCapabilities(t, got.RequiredCapabilities, project.RequiredCapabilities)
	})
}

func TestBotCreateTask(t *testing.T) {
	ctx := t.Context()
	bot := newTestBot(t)
	yuuka := Member{ID: "123", Name: "HayaseYuuka", Available: true, Capabilities: []Capability{FormFilling}}
	hina := Member{ID: "456", Name: "SorasakiHina", Available: false, Capabilities: []Capability{Administrator}}
	project := Project{UUID: "test-project", Name: "test", RequiredCapabilities: []Capability{FormFilling}}
	requireNoError(t, bot.CreateMember(ctx, &yuuka))
	requireNoError(t, bot.CreateMember(ctx, &hina))
	requireNoError(t, bot.CreateProject(ctx, &project))
	missingMember := "missing"

	for _, tt := range []struct {
		name       string
		projectID  string
		memberID   *string
		status     TaskStatus
		wantStatus TaskStatus
		wantErr    error
		wantCode   sqlite3.ErrNoExtended
	}{
		{name: "available member defaults to allocated", projectID: project.UUID, memberID: &yuuka.ID, wantStatus: TaskAllocated},
		// 对应 build/test.go：省略状态时默认 allocated，未分配成员会违反分配约束。
		{name: "unassigned task with default status is rejected", projectID: project.UUID, wantCode: sqlite3.ErrConstraintCheck},
		{name: "unassigned allocated task is rejected", projectID: project.UUID, status: TaskAllocated, wantCode: sqlite3.ErrConstraintCheck},
		// pending / cancelled 可由成员主动拒绝分配产生，保留这两种状态允许无成员的约束例外。
		{name: "unassigned pending task", projectID: project.UUID, status: TaskPending, wantStatus: TaskPending},
		{name: "unassigned cancelled task", projectID: project.UUID, status: TaskCancelled, wantStatus: TaskCancelled},
		{name: "unavailable member is rejected", projectID: project.UUID, memberID: &hina.ID, wantCode: sqlite3.ErrConstraintTrigger},
		{name: "missing member is rejected", projectID: project.UUID, memberID: &missingMember, wantCode: sqlite3.ErrConstraintTrigger},
		{name: "missing project is rejected", projectID: "missing", memberID: &yuuka.ID, wantErr: gorm.ErrForeignKeyViolated},
	} {
		t.Run(tt.name, func(t *testing.T) {
			task := Task{UUID: tt.name, ProjectID: tt.projectID, AllocatedMemberID: tt.memberID, Status: tt.status, RelatedPath: []string{"forms/request.json", "表单/附件.txt"}}
			err := bot.CreateTask(ctx, &task)
			if tt.wantErr != nil || tt.wantCode != 0 {
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("CreateTask = %v, want %v", err, tt.wantErr)
				}
				if tt.wantCode != 0 {
					var sqliteErr sqlite3.Error
					if !errors.As(err, &sqliteErr) || sqliteErr.ExtendedCode != tt.wantCode {
						t.Fatalf("CreateTask = %v, want SQLite code %v", err, tt.wantCode)
					}
					if tt.wantCode == sqlite3.ErrConstraintCheck && !strings.Contains(err.Error(), "task_allocation_check") {
						t.Fatalf("error does not identify the allocation constraint: %v", err)
					}
					if tt.wantCode == sqlite3.ErrConstraintTrigger && tt.memberID != nil && !strings.Contains(err.Error(), *tt.memberID) {
						t.Fatalf("error does not identify member %q: %v", *tt.memberID, err)
					}
				}
				var count int64
				requireNoError(t, bot.db.Model(&Task{}).Where("uuid = ?", task.UUID).Count(&count).Error)
				if count != 0 {
					t.Fatalf("failed creation persisted %d tasks", count)
				}
				return
			}
			requireNoError(t, err)
			// 尚无任务读取接口，直接读库验证创建结果和 JSON 字段持久化。
			var got Task
			requireNoError(t, bot.db.Where("uuid = ?", task.UUID).First(&got).Error)
			if got.ProjectID != project.UUID || got.Status != tt.wantStatus {
				t.Fatalf("stored task = %+v, want project %q and status %q", got, project.UUID, tt.wantStatus)
			}
			if tt.memberID == nil {
				if got.AllocatedMemberID != nil {
					t.Fatalf("unexpected allocated member: %q", *got.AllocatedMemberID)
				}
			} else if got.AllocatedMemberID == nil || *got.AllocatedMemberID != *tt.memberID {
				t.Fatalf("stored task = %+v, want member %q", got, *tt.memberID)
			}
			if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
				t.Fatalf("task timestamps were not populated: %+v", got)
			}
			if !slices.Equal(got.RelatedPath, task.RelatedPath) {
				t.Fatalf("RelatedPath = %v, want %v", got.RelatedPath, task.RelatedPath)
			}
		})
	}
}
