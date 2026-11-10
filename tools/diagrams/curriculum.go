package main

import (
	"encoding/json"
	"errors"
	"os"
)

type tuple struct{ Subject, Relation, Object string }

func readCurriculum(root string) (tuples []tuple, err error) {
	fs, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, fs.Close()) }()
	data, err := fs.ReadFile("internal/graph/seed.json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &tuples); err != nil {
		return nil, err
	}
	return tuples, nil
}
