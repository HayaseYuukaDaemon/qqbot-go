package kivo

import (
	"encoding/json"
	"time"
)

type Capability string

const (
	FormFilling   Capability = "form_filling"
	Translator    Capability = "translator"
	Assistant     Capability = "assistant"
	Administrator Capability = "admin"
)

type MemberCapability struct {
	MemberID   string     `gorm:"primaryKey"`
	Capability Capability `gorm:"primaryKey"`
}

type Member struct {
	Name      string `json:"name"`
	ID        string `json:"id" gorm:"primaryKey"` // 设计为qq号
	Available bool   `json:"available"`

	CapabilityRows []MemberCapability `gorm:"foreignKey:MemberID" json:"-"`
}

func (m *Member) MarshalJSON() ([]byte, error) {
	type aliasMember Member

	aux := struct {
		*aliasMember
		Capabilities []Capability `json:"capabilities"`
	}{
		aliasMember: (*aliasMember)(m),
	}

}

func (m *Member) UnmarshalJSON(b []byte) error {
	type aliasMember Member

	aux := struct {
		*aliasMember
		Capabilities []Capability `json:"capabilities"`
	}{
		aliasMember: (*aliasMember)(m),
	}

	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}

	m.CapabilityRows = make([]MemberCapability, len(aux.Capabilities))
	for i, cap := range aux.Capabilities {
		m.CapabilityRows[i] = MemberCapability{
			MemberID:   m.ID,
			Capability: cap,
		}
	}

	return nil
}

func (m Member) Capabilities() []Capability {
	result := make([]Capability, 0, len(m.CapabilityRows))

	for _, c := range m.CapabilityRows {
		result = append(result, c.Capability)
	}

	return result
}

type Project struct {
	UUID                 string       `json:"uuid" gorm:"primaryKey"`
	Name                 string       `json:"name"`
	RequiredCapabilities []Capability `json:"required_capabilities"`
	Desc                 string       `json:"desc"`
}

type ProjectSet struct {
	UUID     string    `json:"uuid" gorm:"primaryKey"`
	Name     string    `json:"name"`
	Projects []Project `json:"projects"`

	// 周期属性
	StartAt  *time.Time     `json:"start_at"`
	EndAt    *time.Time     `json:"end_at"`
	Interval *time.Duration `json:"interval"`
}

type TaskStatus string

const (
	TaskCreated   TaskStatus = "created"
	TaskAllocated TaskStatus = "allocated"
	TaskAccepted  TaskStatus = "accepted"
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskBlocked   TaskStatus = "blocked"
	TaskCancelled TaskStatus = "cancelled"
	TaskCompleted TaskStatus = "completed"
)

type Task struct {
	UUID              string     `json:"uuid" gorm:"primaryKey"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at" `
	ProjectID         string     `json:"project_id"`
	Status            TaskStatus `json:"status"`
	AllocatedMemberID string     `json:"allocated_member_id"`
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
	ReceivedAt time.Time  `json:"received_at"`
	MemberID   string     `json:"member_id"` // 如果为空则说明是由Bot发起
	Type       MotionType `json:"type"`
	RawContent string     `json:"raw_content"`
	ContentPtr string     `json:"content_ptr"` // 可空
}
