// Package advisor provides a read-only, evidence-grounded research advisor for
// the dashboard. It never writes market data, generates SQL, browses the web,
// or exposes hidden model reasoning.
package advisor

import (
	"embed"
	"fmt"
	"strings"
)

// Role is a versioned analytical lens. A role is not a claim that the named
// person said, endorsed, or would say anything in a generated answer.
type Role struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	PromptVersion string `json:"prompt_version"`
}

// DefaultRoleID is used when a request omits a role.
const DefaultRoleID = "buffett"

const rolePromptVersion = "role-v1"

//go:embed prompts/*.md
var promptFiles embed.FS

type roleSpec struct {
	Role
	file string
}

var roleSpecs = []roleSpec{
	{Role: Role{ID: "buffett", Name: "巴菲特", Description: "从企业质量、护城河、长期现金流和安全边际审视问题。", PromptVersion: rolePromptVersion}, file: "buffett.md"},
	{Role: Role{ID: "munger", Name: "芒格", Description: "从多元思维模型、逆向排除和认知偏差审视问题。", PromptVersion: rolePromptVersion}, file: "munger.md"},
	{Role: Role{ID: "graham", Name: "格雷厄姆", Description: "从可验证资产、估值保护、分散与本金风险审视问题。", PromptVersion: rolePromptVersion}, file: "graham.md"},
	{Role: Role{ID: "livermore", Name: "利弗莫尔", Description: "从趋势确认、关键点位、资金管理和纪律审视问题。", PromptVersion: rolePromptVersion}, file: "livermore.md"},
	{Role: Role{ID: "soros", Name: "索罗斯", Description: "从反身性、预期差、证伪条件和风险暴露审视问题。", PromptVersion: rolePromptVersion}, file: "soros.md"},
	{Role: Role{ID: "simons", Name: "西蒙斯", Description: "从统计规律、样本稳定性、执行一致性和过拟合风险审视问题。", PromptVersion: rolePromptVersion}, file: "simons.md"},
	{Role: Role{ID: "lynch", Name: "彼得·林奇", Description: "从业务可理解性、增长质量、分类研究和常识校验审视问题。", PromptVersion: rolePromptVersion}, file: "lynch.md"},
	{Role: Role{ID: "duan-yongping", Name: "段永平", Description: "从生意本质、用户价值、长期主义和不做错事的边界审视问题。", PromptVersion: rolePromptVersion}, file: "duan-yongping.md"},
}

var roleIndex = func() map[string]int {
	index := make(map[string]int, len(roleSpecs))
	for i, spec := range roleSpecs {
		index[spec.ID] = i
	}
	return index
}()

// Roles returns the eight built-in roles in stable display order.
func Roles() []Role {
	roles := make([]Role, 0, len(roleSpecs))
	for _, spec := range roleSpecs {
		roles = append(roles, spec.Role)
	}
	return roles
}

// RoleByID resolves a role without exposing mutable package state.
func RoleByID(id string) (Role, bool) {
	i, ok := roleIndex[strings.TrimSpace(id)]
	if !ok {
		return Role{}, false
	}
	return roleSpecs[i].Role, true
}

func readPrompt(name string) string {
	raw, err := promptFiles.ReadFile("prompts/" + name)
	if err != nil {
		panic(fmt.Sprintf("advisor: embedded prompt %s is missing", name))
	}
	return strings.TrimSpace(string(raw))
}

func rolePrompt(id string) (Role, string, error) {
	if strings.TrimSpace(id) == "" {
		id = DefaultRoleID
	}
	i, ok := roleIndex[id]
	if !ok {
		return Role{}, "", ErrUnknownRole
	}
	return roleSpecs[i].Role, readPrompt(roleSpecs[i].file), nil
}
