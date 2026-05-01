package ssr

import (
	"reflect"
	"strings"
)

func injectParamsReflect(page Page, params map[string]string) {
	if len(params) == 0 {
		return
	}

	v := reflect.ValueOf(page)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	fieldMap := make(map[string]int, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fieldMap[strings.ToLower(f.Name)] = i
	}
	for key, val := range params {
		idx, ok := fieldMap[strings.ToLower(key)]
		if !ok {
			continue
		}
		fv := v.Field(idx)
		if fv.Kind() == reflect.String && fv.CanSet() {
			fv.SetString(val)
		}
	}
}
