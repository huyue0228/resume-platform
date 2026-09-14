// Package contract 校验随版本发布的协议快照，不依赖兄弟仓库或在线 Schema。
package contract

import (
	"embed"
	"encoding/json"
	"errors"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed bundle/*
var Bundle embed.FS
var once sync.Once
var schemas map[string]*jsonschema.Resolved
var initError error

func Validate(name string, raw []byte) error {
	once.Do(func() {
		schemas = map[string]*jsonschema.Resolved{}
		for _, n := range []string{"request", "response", "capabilities", "allocation.request", "allocation.response", "allocation.capabilities"} {
			data, err := Bundle.ReadFile("bundle/" + n + ".schema.json")
			if err != nil {
				initError = err
				return
			}
			var schema jsonschema.Schema
			if err = json.Unmarshal(data, &schema); err != nil {
				initError = err
				return
			}
			schemas[n], err = schema.Resolve(nil)
			if err != nil {
				initError = err
				return
			}
		}
	})
	if initError != nil {
		return initError
	}
	schema, ok := schemas[name]
	if !ok {
		return errors.New("unknown contract")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return schema.Validate(value)
}
