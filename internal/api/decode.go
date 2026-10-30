package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func decodeStrictJSON(body []byte, req *domain.AdaptivePlanRequest) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return errors.New("request body must be valid JSON")
	}
	if body[0] != '{' {
		return errors.New("request body must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := scanJSONValue(dec, reflect.TypeFor[domain.AdaptivePlanRequest](), "", false); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("request body must contain exactly one JSON value")
	}
	dec = json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(req); err != nil {
		return errors.New("request body must match the JSON schema")
	}
	return nil
}

func scanJSONValue(dec *json.Decoder, typ reflect.Type, field string, element bool) error {
	token, err := dec.Token()
	if err != nil {
		return errors.New("request body must be valid JSON")
	}
	if token == nil && element {
		return fmt.Errorf("%s must not contain null elements", field)
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch token {
	case json.Delim('{'):
		if typ.Kind() != reflect.Struct {
			return errors.New("request body must match the JSON schema")
		}
		fields := make(map[string]reflect.Type, typ.NumField())
		for i := range typ.NumField() {
			f := typ.Field(i)
			fields[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Type
		}
		seen := make(map[string]bool, len(fields))
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return errors.New("request body must be valid JSON")
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("request body must be valid JSON")
			}
			if seen[name] {
				return fmt.Errorf("duplicate field %q", boundedFieldName(name))
			}
			seen[name] = true
			fieldType, ok := fields[name]
			if !ok {
				return fmt.Errorf("unknown field %q", boundedFieldName(name))
			}
			if err := scanJSONValue(dec, fieldType, name, false); err != nil {
				return err
			}
		}
	case json.Delim('['):
		if typ.Kind() != reflect.Slice {
			return errors.New("request body must match the JSON schema")
		}
		for dec.More() {
			if err := scanJSONValue(dec, typ.Elem(), field, true); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("request body must be valid JSON")
	}
	return nil
}

func boundedFieldName(name string) string {
	runes := []rune(name)
	if len(runes) > 64 {
		return string(runes[:64]) + "…"
	}
	return name
}
