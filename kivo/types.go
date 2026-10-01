package kivo

import (
	"log/slog"
	"time"
)

type BotConfig struct {
	DBPath string `json:"db_path"`
	Debug  bool   `json:"debug"`

	// 运行时配置
	Logger *slog.Logger `json:"-"`
}

type Capability struct {
	Name string `json:"name" gorm:"name"`
}

type Member struct {
	Name string `json:"name" gorm:"unique"`
	ID   string `json:"id" gorm:"primaryKey"` // 设计为qq号

	Capabilities []Capability
}

type IsAvailable interface {
	IsAvailable() bool
}

// 再加一个保存member不可用时间表的数据表

type Project struct {
	UUID string `json:"uuid" gorm:"primaryKey"`
	Name string `json:"name"`
	Desc string `json:"desc"`

	RequiredCapabilities []Capability `json:"required_capabilities"`

	// 周期属性
	StartAt  *time.Time     `json:"start_at"` // 项目开始于
	EndAt    *time.Time     `json:"end_at"`   // 项目终止于
	Interval *time.Duration `json:"interval"` // 周期性项目的Interval

	// task 属性
	OfferingExpire *time.Duration `json:"offering_expire"` // 单次分配允许等待的最大时长
	Deadline       *time.Duration `json:"deadline"`        // 自对应 task 创建开始, 在此时间内必须完成

	RelatedPaths []string `json:"related_paths" gorm:"serializer:json;type:text"`
}

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskAssigned  TaskStatus = "assigned"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskCancelled TaskStatus = "cancelled"
	TaskFailed    TaskStatus = "failed"
)

type Task struct {
	UUID      string     `json:"uuid" gorm:"primaryKey"`
	Desc      string     `json:"desc"`
	CreatedAt time.Time  `json:"created_at"` // 任务创建即视为开始
	UpdatedAt time.Time  `json:"updated_at"` // 状态等变更
	Status    TaskStatus `json:"status" gorm:"not null;default:null"`

	ProjectID string  `json:"-" gorm:"not null;default:null"`
	Project   Project `json:"project" gorm:"foreignKey:ProjectID;references:UUID;constraint:OnDelete:RESTRICT"`

	RelatedPath []string `json:"related_path" gorm:"serializer:json;type:text"`
}

type AllocationStatus string

const (
	AllocationOffered   AllocationStatus = "offered"
	AllocationAccepted  AllocationStatus = "accepted"
	AllocationRejected  AllocationStatus = "rejected"
	AllocationExpired   AllocationStatus = "expired"
	AllocationCancelled AllocationStatus = "cancelled"
)

type Allocation struct {
	ID        string           `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Status    AllocationStatus `json:"status" gorm:"not null;default:offered"`

	MemberID string `json:"-" gorm:"not null;default:null"`
	Member   Member `json:"member" gorm:"foreignKey:MemberID;references:ID;constraint:OnDelete:RESTRICT"`

	TaskID string `json:"-" gorm:"not null;default:null"`
	Task   Task   `json:"task" gorm:"foreignKey:TaskID;references:UUID;constraint:OnDelete:RESTRICT"`
}

type MotionType string

const (
	Notify         MotionType = "notify"
	AcceptAllocate MotionType = "accept_allocate"
	RejectAllocate MotionType = "reject_allocate"
	SwitchRequest  MotionType = "switch_request"
	ReserveRequest MotionType = "reserve_request"
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
