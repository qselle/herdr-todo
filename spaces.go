package main

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

type Space struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Session     string `json:"session"`
}

func validateSpace(s *Space) error {
	if s != nil && (s.WorkspaceID == "" || s.Label == "" || s.Session == "") {
		return errors.New("Choose a space from Herdr, or leave it unattached")
	}
	return nil
}

func sameSpace(a, b *Space) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

type SpaceSource interface {
	Refresh() ([]Space, error)
	Label(*Space) string
	Focus(*Space) error
}

type HerdrSpaces struct {
	context Context
	items   map[string]Space
}

func (s *HerdrSpaces) command(args ...string) (json.RawMessage, error) {
	if os.Getenv("HERDR_ENV") != "1" || s.context.Session == "" {
		return nil, errors.New("Open Tasks inside Herdr to choose a space")
	}
	env := append(os.Environ(), "HERDR_SOCKET_PATH="+s.context.Session)
	out, err := herdrCommand(3*time.Second, env, append([]string{"workspace"}, args...)...)
	if err != nil {
		return nil, errors.New("Couldn't reach that space. Choose it again from the list.")
	}
	var data struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &data); err != nil || len(data.Result) == 0 || string(data.Result) == "null" {
		return nil, errors.New("Herdr returned an unreadable space list")
	}
	return data.Result, nil
}

func (s *HerdrSpaces) Refresh() ([]Space, error) {
	raw, err := s.command("list")
	if err != nil {
		return nil, err
	}
	var data struct {
		Workspaces []Space `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || data.Workspaces == nil {
		return nil, errors.New("Herdr returned an unreadable space list")
	}
	items := make(map[string]Space)
	for i := range data.Workspaces {
		link := &data.Workspaces[i]
		link.Session = s.context.Session
		if err := validateSpace(link); err != nil {
			return nil, errors.New("Herdr returned an unreadable space list")
		}
		items[link.WorkspaceID] = *link
	}
	s.items = items
	return data.Workspaces, nil
}

func (s *HerdrSpaces) Label(link *Space) string {
	if link == nil {
		return "None"
	}
	if link.Session == s.context.Session {
		if item, ok := s.items[link.WorkspaceID]; ok {
			return item.Label
		}
	}
	return link.Label
}

func (s *HerdrSpaces) Focus(link *Space) error {
	if err := validateSpace(link); err != nil {
		return err
	}
	if link == nil {
		return errors.New("Attach a space first")
	}
	if link.Session != s.context.Session {
		return errors.New("This space belongs to another Herdr session. Attach one from this session to open it here.")
	}
	if _, err := s.Refresh(); err != nil {
		return err
	}
	if _, ok := s.items[link.WorkspaceID]; !ok {
		return errors.New("This space is closed. You can attach another one.")
	}
	_, err := s.command("focus", link.WorkspaceID)
	return err
}
