package providers

import "reflect"

// deepCopyJSONValue copies a JSON-encodable value tree so that nothing
// reachable from the result is shared with the original. Maps, slices,
// arrays, pointers and exported struct fields are copied all the way
// down with their concrete types kept, so the copy encodes byte for byte
// like the original. A caller can therefore hand out the copy (to a wire
// observer, or keep it as configuration) and no later write through
// either side, including through a typed []string or map[string]string,
// reaches the other. Scalars, strings and funcs are returned as they
// are, being immutable or never encoded.
func deepCopyJSONValue(v any) any {
	switch typed := v.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[k] = deepCopyJSONValue(val)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, val := range typed {
			out[i] = deepCopyJSONValue(val)
		}
		return out
	}
	return deepCopyReflect(reflect.ValueOf(v)).Interface()
}

// deepCopyReflect is the typed-value half of [deepCopyJSONValue]: the two
// fast paths above cover what YAML config decodes to, and this covers any
// other map, slice, array, pointer or struct a programmatic caller passes.
func deepCopyReflect(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopyReflect(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(deepCopyReflect(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			out.Index(i).Set(deepCopyReflect(v.Index(i)))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopyReflect(v.Elem()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopyReflect(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			if field := out.Field(i); field.CanSet() {
				field.Set(deepCopyReflect(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}
