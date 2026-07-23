package requestpolicy

import (
	"errors"
	"fmt"
	"math"
	"reflect"
)

type cloneVisit struct {
	kind reflect.Kind
	typ  reflect.Type
	ptr  uintptr
	len  int
	cap  int
}

// CloneJSONValue returns an isolated copy of a JSON-native value while
// preserving named map, slice, array, and scalar types. Unsupported values,
// cycles, and non-finite floats are rejected deterministically.
func CloneJSONValue(value interface{}) (interface{}, error) {
	return cloneJSONValue(value, "$", make(map[cloneVisit]struct{}))
}

func cloneJSONValue(value interface{}, path string, active map[cloneVisit]struct{}) (interface{}, error) {
	if value == nil {
		return nil, nil
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.String:
		return value, nil
	case reflect.Float32, reflect.Float64:
		if number := reflected.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("%s: non-finite floating-point value is not JSON-compatible", path)
		}
		return value, nil
	case reflect.Map:
		if reflected.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s: unsupported map key type %s", path, reflected.Type().Key())
		}
		if reflected.IsNil() {
			return value, nil
		}
		visit := cloneVisit{kind: reflect.Map, typ: reflected.Type(), ptr: reflected.Pointer()}
		if _, exists := active[visit]; exists {
			return nil, fmt.Errorf("%s: %w", path, errors.New("cyclic map value is not JSON-compatible"))
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		clone := reflect.MakeMapWithSize(reflected.Type(), reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			key := iterator.Key()
			item, err := cloneJSONValue(iterator.Value().Interface(), path+"/"+fmt.Sprint(key.Interface()), active)
			if err != nil {
				return nil, err
			}
			clone.SetMapIndex(key, reflectValueFor(item, reflected.Type().Elem()))
		}
		return clone.Interface(), nil
	case reflect.Slice:
		if reflected.IsNil() {
			return value, nil
		}
		visit := cloneVisit{
			kind: reflect.Slice,
			typ:  reflected.Type(),
			ptr:  reflected.Pointer(),
			len:  reflected.Len(),
			cap:  reflected.Cap(),
		}
		if _, exists := active[visit]; exists {
			return nil, fmt.Errorf("%s: %w", path, errors.New("cyclic slice value is not JSON-compatible"))
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		clone := reflect.MakeSlice(reflected.Type(), reflected.Len(), reflected.Len())
		for index := range reflected.Len() {
			item, err := cloneJSONValue(reflected.Index(index).Interface(), fmt.Sprintf("%s/%d", path, index), active)
			if err != nil {
				return nil, err
			}
			clone.Index(index).Set(reflectValueFor(item, reflected.Type().Elem()))
		}
		return clone.Interface(), nil
	case reflect.Array:
		clone := reflect.New(reflected.Type()).Elem()
		for index := range reflected.Len() {
			item, err := cloneJSONValue(reflected.Index(index).Interface(), fmt.Sprintf("%s/%d", path, index), active)
			if err != nil {
				return nil, err
			}
			clone.Index(index).Set(reflectValueFor(item, reflected.Type().Elem()))
		}
		return clone.Interface(), nil
	default:
		return nil, fmt.Errorf("%s: unsupported JSON-compatible value type %T", path, value)
	}
}

func reflectValueFor(value interface{}, target reflect.Type) reflect.Value {
	if value == nil {
		return reflect.Zero(target)
	}
	return reflect.ValueOf(value)
}
