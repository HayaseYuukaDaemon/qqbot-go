package kivo

import (
	"errors"
	"testing"

	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

func TestBotForeignKeySchema(t *testing.T) {
	bot := newTestBot(t)
	var enabled int
	requireNoError(t, bot.db.Raw("PRAGMA foreign_keys").Scan(&enabled).Error)
	if enabled != 1 {
		t.Fatal("SQLite foreign key enforcement is disabled")
	}
	for _, tt := range []struct {
		table, column, parent, parentColumn, onDelete string
	}{
		{"tasks", "project_id", "projects", "uuid", "RESTRICT"},
		{"tasks", "allocated_member_id", "members", "id", "SET NULL"},
		{"member_capabilities", "member_id", "members", "id", "NO ACTION"},
		{"project_capabilities", "project_id", "projects", "uuid", "NO ACTION"},
		{"motions", "member_id", "members", "id", "NO ACTION"},
	} {
		t.Run(tt.table+"/"+tt.column, func(t *testing.T) {
			var keys []struct {
				Table, From, To, OnUpdate, OnDelete string
			}
			requireNoError(t, bot.db.Raw("PRAGMA foreign_key_list("+tt.table+")").Scan(&keys).Error)
			for _, key := range keys {
				if key.From == tt.column {
					if key.Table != tt.parent || key.To != tt.parentColumn || key.OnDelete != tt.onDelete || key.OnUpdate != "NO ACTION" {
						t.Fatalf("foreign key = %+v, want %+v with ON UPDATE NO ACTION", key, tt)
					}
					t.Logf("%s.%s -> %s.%s: DELETE %s, UPDATE %s", tt.table, key.From, key.Table, key.To, key.OnDelete, key.OnUpdate)
					return
				}
			}
			t.Fatalf("missing foreign key for %s.%s", tt.table, tt.column)
		})
	}
}

func TestBotForeignKeysPreventDanglingReferences(t *testing.T) {
	// 使用原始 SQL，确认约束由数据库执行，不依赖 GORM 自动保存关联对象。
	for _, tt := range []struct {
		name   string
		setup  string
		mutate string
		code   sqlite3.ErrNoExtended
	}{
		{"task insert missing project", "", "INSERT INTO tasks(uuid, project_id, status) VALUES ('t', 'missing', 'pending')", 0},
		{"task update missing project", "INSERT INTO tasks(uuid, project_id, status) VALUES ('t', 'p', 'pending')", "UPDATE tasks SET project_id = 'missing' WHERE uuid = 't'", 0},
		{"task insert missing member", "", "INSERT INTO tasks(uuid, project_id, allocated_member_id) VALUES ('t', 'p', 'missing')", sqlite3.ErrConstraintTrigger},
		{"task update missing member", "INSERT INTO tasks(uuid, project_id, allocated_member_id) VALUES ('t', 'p', 'm')", "UPDATE tasks SET allocated_member_id = 'missing' WHERE uuid = 't'", sqlite3.ErrConstraintTrigger},
		{"member capability insert missing member", "", "INSERT INTO member_capabilities(member_id, capability) VALUES ('missing', 'assistant')", 0},
		{"member capability update missing member", "INSERT INTO member_capabilities(member_id, capability) VALUES ('m', 'assistant')", "UPDATE member_capabilities SET member_id = 'missing'", 0},
		{"project capability insert missing project", "", "INSERT INTO project_capabilities(project_id, capability) VALUES ('missing', 'assistant')", 0},
		{"project capability update missing project", "INSERT INTO project_capabilities(project_id, capability) VALUES ('p', 'assistant')", "UPDATE project_capabilities SET project_id = 'missing'", 0},
		{"motion insert missing member", "", "INSERT INTO motions(uuid, member_id) VALUES ('motion', 'missing')", 0},
		{"motion update missing member", "INSERT INTO motions(uuid, member_id) VALUES ('motion', 'm')", "UPDATE motions SET member_id = 'missing'", 0},
		{"delete project referenced by task", "INSERT INTO tasks(uuid, project_id, status) VALUES ('t', 'p', 'pending')", "DELETE FROM projects WHERE uuid = 'p'", sqlite3.ErrConstraintTrigger},
		{"rename project referenced by task", "INSERT INTO tasks(uuid, project_id, status) VALUES ('t', 'p', 'pending')", "UPDATE projects SET uuid = 'new-p' WHERE uuid = 'p'", 0},
		{"rename member referenced by task", "INSERT INTO tasks(uuid, project_id, allocated_member_id) VALUES ('t', 'p', 'm')", "UPDATE members SET id = 'new-m' WHERE id = 'm'", 0},
		{"delete member referenced by capability", "INSERT INTO member_capabilities(member_id, capability) VALUES ('m', 'assistant')", "DELETE FROM members WHERE id = 'm'", 0},
		{"rename member referenced by capability", "INSERT INTO member_capabilities(member_id, capability) VALUES ('m', 'assistant')", "UPDATE members SET id = 'new-m' WHERE id = 'm'", 0},
		{"delete project referenced by capability", "INSERT INTO project_capabilities(project_id, capability) VALUES ('p', 'assistant')", "DELETE FROM projects WHERE uuid = 'p'", 0},
		{"rename project referenced by capability", "INSERT INTO project_capabilities(project_id, capability) VALUES ('p', 'assistant')", "UPDATE projects SET uuid = 'new-p' WHERE uuid = 'p'", 0},
		{"delete member referenced by motion", "INSERT INTO motions(uuid, member_id) VALUES ('motion', 'm')", "DELETE FROM members WHERE id = 'm'", 0},
		{"rename member referenced by motion", "INSERT INTO motions(uuid, member_id) VALUES ('motion', 'm')", "UPDATE members SET id = 'new-m' WHERE id = 'm'", 0},
		{"task project cannot be null", "", "INSERT INTO tasks(uuid, status) VALUES ('t', 'pending')", sqlite3.ErrConstraintNotNull},
		{"allocated task member cannot be null", "", "INSERT INTO tasks(uuid, project_id) VALUES ('t', 'p')", sqlite3.ErrConstraintCheck},
		{"delete member assigned to allocated task", "INSERT INTO tasks(uuid, project_id, allocated_member_id) VALUES ('t', 'p', 'm')", "DELETE FROM members WHERE id = 'm'", sqlite3.ErrConstraintCheck},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bot := newTestBot(t)
			requireNoError(t, bot.db.Exec("INSERT INTO members(id, name, available) VALUES ('m', 'member', 1)").Error)
			requireNoError(t, bot.db.Exec("INSERT INTO projects(uuid, name) VALUES ('p', 'project')").Error)
			if tt.setup != "" {
				requireNoError(t, bot.db.Exec(tt.setup).Error)
			}
			err := bot.db.Exec(tt.mutate).Error
			if tt.code == 0 {
				if !errors.Is(err, gorm.ErrForeignKeyViolated) {
					t.Fatalf("mutation = %v, want foreign key violation", err)
				}
			} else {
				var sqliteErr sqlite3.Error
				if !errors.As(err, &sqliteErr) || sqliteErr.ExtendedCode != tt.code {
					t.Fatalf("mutation = %v, want SQLite code %v", err, tt.code)
				}
			}
			var violations []map[string]any
			requireNoError(t, bot.db.Raw("PRAGMA foreign_key_check").Scan(&violations).Error)
			if len(violations) != 0 {
				t.Fatalf("dangling references: %v", violations)
			}
		})
	}
}

func TestBotDeleteMemberClearsOptionalTaskAssignment(t *testing.T) {
	for _, status := range []TaskStatus{TaskPending, TaskCancelled} {
		t.Run(string(status), func(t *testing.T) {
			bot := newTestBot(t)
			requireNoError(t, bot.db.Exec("INSERT INTO members(id, name, available) VALUES ('m', 'member', 1)").Error)
			requireNoError(t, bot.db.Exec("INSERT INTO projects(uuid, name) VALUES ('p', 'project')").Error)
			requireNoError(t, bot.db.Exec("INSERT INTO tasks(uuid, project_id, allocated_member_id, status) VALUES ('t', 'p', 'm', ?)", status).Error)
			requireNoError(t, bot.db.Exec("DELETE FROM members WHERE id = 'm'").Error)
			var task Task
			requireNoError(t, bot.db.First(&task).Error)
			if task.AllocatedMemberID != nil || task.Status != status {
				t.Fatalf("task after deleting member = %+v", task)
			}
		})
	}
}
