package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

type reply struct {
	ID   int64     `json:"id"`
	Text string    `json:"text"`
	Date time.Time `json:"date"`
}

type store struct {
	dir string
}

func (s store) path(id int64, name string) string {
	return filepath.Join(s.dir, strconv.FormatInt(id, 10), name+".json")
}

func (s store) setOwner(id int64, agent string) error {
	return writeJSONFile(s.path(id, "owner"), agent)
}

func (s store) owner(id int64) (string, error) {
	var owner string
	err := readJSONFile(s.path(id, "owner"), &owner)
	return owner, err
}

func (s store) replies(id int64) ([]reply, error) {
	replies := []reply{}
	err := readJSONFile(s.path(id, "replies"), &replies)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return replies, err
}

// The Telegram polling loop is the sole writer of replies.
func (s store) addReply(parentID int64, incoming reply) error {
	replies, err := s.replies(parentID)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(replies, func(existing reply) bool { return existing.ID == incoming.ID }) {
		return nil
	}
	return writeJSONFile(s.path(parentID, "replies"), append(replies, incoming))
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeJSONFile(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(value)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
