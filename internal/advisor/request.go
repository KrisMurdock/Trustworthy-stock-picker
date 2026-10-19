package advisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// UnmarshalJSON accepts both the documented request shape and the dashboard's
// role_id/messages shape. The last user message becomes Question; earlier
// messages become History. Unknown fields are rejected.
func (r *Request) UnmarshalJSON(data []byte) error {
	var wire struct {
		Mode     string `json:"mode,omitempty"`
		Role     string `json:"role,omitempty"`
		RoleID   string `json:"role_id,omitempty"`
		Question string `json:"question,omitempty"`
		AsOf     string `json:"as_of,omitempty"`
		Code     string `json:"code,omitempty"`
		Revision string `json:"revision,omitempty"`
		History  []Turn `json:"history,omitempty"`
		Messages []Turn `json:"messages,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("advisor request must contain one JSON object")
	}
	role := wire.Role
	if role == "" {
		role = wire.RoleID
	}
	question := wire.Question
	history := append([]Turn(nil), wire.History...)
	if len(wire.Messages) > 0 {
		lastUser := -1
		for i := len(wire.Messages) - 1; i >= 0; i-- {
			if strings.EqualFold(strings.TrimSpace(wire.Messages[i].Role), "user") {
				lastUser = i
				break
			}
		}
		if lastUser >= 0 && (question == "" || strings.TrimSpace(wire.Messages[lastUser].Content) == strings.TrimSpace(question)) {
			if question == "" {
				question = wire.Messages[lastUser].Content
			}
			if len(history) == 0 {
				history = append(history, wire.Messages[:lastUser]...)
			}
		} else if len(history) == 0 {
			history = append(history, wire.Messages...)
		}
	}
	*r = Request{
		Mode:     wire.Mode,
		Role:     role,
		RoleID:   wire.RoleID,
		Question: question,
		AsOf:     wire.AsOf,
		Code:     wire.Code,
		Revision: wire.Revision,
		History:  history,
		Messages: append([]Turn(nil), wire.Messages...),
	}
	return nil
}
