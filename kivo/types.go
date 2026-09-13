package kivo

import (
	"log/slog"
	"time"
)

type Capability string

const (
	FormFilling   Capability = "form_filling"
	Translator    Capability = "translator"
	Assistant     Capability = "assistant"
	Administrator Capability = "admin"
)

type BotConfig struct {
	DBPath string `json:"db_path"`
	Debug  bool   `json:"debug"`

	// 运行时配置
	Logger *slog.Logger `json:"-"`
}

type Member struct {
	Name      string `json:"name" gorm:"unique"`
	ID        string `json:"id" gorm:"primaryKey"` // 设计为qq号
	Available bool   `json:"available"`

	Capabilities []Capability `gorm:"-"`
}

type memberCapability struct {
	MemberID   string     `gorm:"primaryKey"`
	Capability Capability `gorm:"primaryKey"`

	Member Member `gorm:"foreignKey:MemberID"`
}

type Project struct {
	UUID string `json:"uuid" gorm:"primaryKey"`
	Name string `json:"name"`
	Desc string `json:"desc"`

	RequiredCapabilities []Capability `json:"required_capabilities" gorm:"-"`

	// 周期属性
	StartAt  *time.Time     `json:"start_at"` // 项目开始于
	EndAt    *time.Time     `json:"end_at"`   // 项目终止于
	Interval *time.Duration `json:"interval"` // 周期性项目的Interval
}

type projectCapability struct {
	ProjectID  string     `gorm:"primaryKey"`
	Capability Capability `gorm:"primaryKey"`

	Project Project `gorm:"foreignKey:ProjectID;references:UUID"`
}

type TaskStatus string

const (
	TaskAllocated TaskStatus = "allocated"
	TaskAccepted  TaskStatus = "accepted"
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskBlocked   TaskStatus = "blocked"
	TaskCancelled TaskStatus = "cancelled"
	TaskCompleted TaskStatus = "completed"
)

type Task struct {
	UUID      string     `json:"uuid" gorm:"primaryKey"`
	Desc      string     `json:"desc"`
	CreatedAt time.Time  `json:"created_at"` // 任务创建即视为开始
	UpdatedAt time.Time  `json:"updated_at"` // 状态等变更
	EndAt     time.Time  `json:"end_at"`     // 任务截止日期, 带interval的project直接用created+interval
	Status    TaskStatus `json:"status" gorm:"not null;check:task_status_check,status IN ('allocated','accepted','pending','running','blocked','cancelled','completed');check:task_allocation_check,allocated_member_id IS NOT NULL OR status IN ('pending','cancelled')"`

	ProjectID string  `json:"project_id"`
	Project   Project `json:"project"`

	AllocatedMemberID *string `json:"allocated_member_id"` // 可空, 如果为空则说明未分配
	AllocatedMember   *Member `json:"allocated_member" gorm:"constraint:OnDelete:SET NULL"`

	RelatedPath []string `json:"related_path" gorm:"serializer:json;type:text"`
}

type MotionType string

const (
	Notify          MotionType = "notify"
	AccepetAllocate MotionType = "accept_allocate"
	RejectAllocate  MotionType = "reject_allocate"
	SwitchRequest   MotionType = "switch_request"
)

type Motion struct {
	UUID       string     `json:"uuid" gorm:"primaryKey"`
	CreatedAt  time.Time  `json:"received_at"`
	Type       MotionType `json:"type"`
	RawContent string     `json:"raw_content"`
	ContentPtr string     `json:"content_ptr"` // 可空

	MemberID *string `json:"member_id"` // 如果为空则说明是由Bot发起
	Member   *Member `json:"member"`
}
