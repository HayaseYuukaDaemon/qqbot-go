package kivo

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestBotQueryTasks(t *testing.T) {
	ctx := t.Context()
	bot := newTestBot(t)
	yuuka := Member{ID: "123", Name: "HayaseYuuka", Available: true, Capabilities: []Capability{Assistant}}
	hina := Member{ID: "456", Name: "SorasakiHina", Available: true, Capabilities: []Capability{Translator}}
	requireNoError(t, bot.CreateMember(ctx, &yuuka))
	requireNoError(t, bot.CreateMember(ctx, &hina))
	forms := Project{UUID: "forms", Name: "same-name", RequiredCapabilities: []Capability{FormFilling, Assistant}, RelatedPaths: []string{"forms/template.json"}}
	admin := Project{UUID: "admin", Name: "same-name", RequiredCapabilities: []Capability{FormFilling, Administrator}}
	noCaps := Project{UUID: "no-caps", Name: "no requirements"}
	requireNoError(t, bot.CreateProject(ctx, &forms))
	requireNoError(t, bot.CreateProject(ctx, &admin))
	// 直接准备无能力要求的项目，独立验证查询能保留没有能力关联记录的任务。
	requireNoError(t, bot.db.Create(&noCaps).Error)
	projects := map[string]Project{forms.UUID: forms, admin.UUID: admin, noCaps.UUID: noCaps}
	tasks := []Task{
		{UUID: "forms-yuuka", ProjectID: forms.UUID, AllocatedMemberID: &yuuka.ID},
		{UUID: "forms-hina", ProjectID: forms.UUID, AllocatedMemberID: &hina.ID},
		{UUID: "forms-pending", ProjectID: forms.UUID, Status: TaskPending},
		{UUID: "admin-yuuka", ProjectID: admin.UUID, AllocatedMemberID: &yuuka.ID, Status: TaskCompleted},
		{UUID: "admin-cancelled", ProjectID: admin.UUID, Status: TaskCancelled},
		{UUID: "no-caps-hina", ProjectID: noCaps.UUID, AllocatedMemberID: &hina.ID},
	}
	for i := range tasks {
		tasks[i].Desc = "description: " + tasks[i].UUID
		tasks[i].EndAt = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
		tasks[i].RelatedPath = []string{"任务/" + tasks[i].UUID}
		requireNoError(t, bot.CreateTask(ctx, &tasks[i]))
	}
	// 查询历史任务不因成员停用或能力移除而漏掉记录。
	requireNoError(t, bot.DisableMember(ctx, hina.ID))
	requireNoError(t, bot.RemoveCapability(ctx, hina.ID, Translator))
	hina.Available = false
	hina.Capabilities = nil
	members := map[string]Member{yuuka.ID: yuuka, hina.ID: hina}
	empty, missing := "", "' OR 1=1 --"
	for _, tt := range []struct {
		name   string
		params QueryTaskParams
		want   []Task
	}{
		{"no filters includes all statuses and unassigned tasks", QueryTaskParams{}, tasks},
		{"project ID is exact despite shared names", QueryTaskParams{ProjectID: &forms.UUID}, tasks[:3]},
		{"project without capabilities", QueryTaskParams{ProjectID: &noCaps.UUID}, tasks[5:]},
		{"empty project ID is a condition", QueryTaskParams{ProjectID: &empty}, nil},
		{"missing project ID is bound as a value", QueryTaskParams{ProjectID: &missing}, nil},
		{"allocated member", QueryTaskParams{AllocatedMemberID: &yuuka.ID}, []Task{tasks[0], tasks[3]}},
		{"disabled member without capabilities", QueryTaskParams{AllocatedMemberID: &hina.ID}, []Task{tasks[1], tasks[5]}},
		{"empty member ID does not mean unassigned", QueryTaskParams{AllocatedMemberID: &empty}, nil},
		{"missing member ID", QueryTaskParams{AllocatedMemberID: &missing}, nil},
		{"project and member conditions both match", QueryTaskParams{ProjectID: &forms.UUID, AllocatedMemberID: &hina.ID}, []Task{tasks[1]}},
		{"project conflicts with member", QueryTaskParams{ProjectID: &noCaps.UUID, AllocatedMemberID: &yuuka.ID}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bot.QueryTasks(ctx, tt.params)
			requireNoError(t, err)
			if len(got) != len(tt.want) {
				t.Fatalf("QueryTasks = %+v, want %+v", got, tt.want)
			}
			wantByID := make(map[string]Task, len(tt.want))
			for _, task := range tt.want {
				wantByID[task.UUID] = task
			}
			for _, task := range got {
				want, ok := wantByID[task.UUID]
				if !ok {
					t.Fatalf("unexpected or duplicate task: %+v", task)
				}
				delete(wantByID, task.UUID)
				if task.ProjectID != want.ProjectID || task.Status != want.Status || task.Desc != want.Desc ||
					!task.CreatedAt.Equal(want.CreatedAt) || !task.UpdatedAt.Equal(want.UpdatedAt) || !task.EndAt.Equal(want.EndAt) ||
					!slices.Equal(task.RelatedPath, want.RelatedPath) {
					t.Fatalf("task = %+v, want %+v", task, want)
				}
				project := projects[want.ProjectID]
				if task.Project.UUID != project.UUID || task.Project.Name != project.Name || !slices.Equal(task.Project.RelatedPaths, project.RelatedPaths) {
					t.Fatalf("project = %+v, want %+v", task.Project, project)
				}
				requireCapabilities(t, task.Project.RequiredCapabilities, project.RequiredCapabilities)
				if want.AllocatedMemberID == nil {
					if task.AllocatedMemberID != nil || task.AllocatedMember != nil {
						t.Fatalf("unassigned task has member: %+v", task)
					}
					continue
				}
				member := members[*want.AllocatedMemberID]
				if task.AllocatedMemberID == nil || *task.AllocatedMemberID != member.ID || task.AllocatedMember == nil {
					t.Fatalf("task member missing or incorrect: %+v", task)
				}
				if task.AllocatedMember.ID != member.ID || task.AllocatedMember.Name != member.Name || task.AllocatedMember.Available != member.Available {
					t.Fatalf("member = %+v, want %+v", task.AllocatedMember, member)
				}
				requireCapabilities(t, task.AllocatedMember.Capabilities, member.Capabilities)
			}
		})
	}
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := bot.QueryTasks(ctx, QueryTaskParams{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("QueryTasks = %v, want context cancelled", err)
		}
	})
	t.Run("database not initialized", func(t *testing.T) {
		var bot KivoBot
		if _, err := bot.QueryTasks(t.Context(), QueryTaskParams{}); !errors.Is(err, ErrDBNotInit) {
			t.Fatalf("QueryTasks = %v, want ErrDBNotInit", err)
		}
	})
	t.Run("empty database", func(t *testing.T) {
		bot := newTestBot(t)
		tasks, err := bot.QueryTasks(t.Context(), QueryTaskParams{})
		requireNoError(t, err)
		if len(tasks) != 0 {
			t.Fatalf("QueryTasks = %+v, want no tasks", tasks)
		}
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
			// 直接读库验证创建结果，避免创建测试依赖查询接口的实现。
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
