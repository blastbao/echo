package echo

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

type (
	// Binder is the interface that wraps the Bind method.
	Binder interface {
		Bind(i interface{}, c Context) error
	}

	// DefaultBinder is the default implementation of the Binder interface.
	DefaultBinder struct{}

	// BindUnmarshaler is the interface used to wrap the UnmarshalParam method.
	BindUnmarshaler interface {
		// UnmarshalParam decodes and assigns a value from an form or query param.
		UnmarshalParam(param string) error
	}
)

// Bind implements the `Binder#Bind` function.
func (b *DefaultBinder) Bind(i interface{}, c Context) (err error) {
	req := c.Request()
	if req.ContentLength == 0 {
		if req.Method == http.MethodGet || req.Method == http.MethodDelete {
			if err = b.bindData(i, c.QueryParams(), "query"); err != nil {
				return NewHTTPError(http.StatusBadRequest, err.Error()).SetInternal(err)
			}
			return
		}
		return NewHTTPError(http.StatusBadRequest, "Request body can't be empty")
	}

	ctype := req.Header.Get(HeaderContentType)
	switch {
	case strings.HasPrefix(ctype, MIMEApplicationJSON):
		if err = json.NewDecoder(req.Body).Decode(i); err != nil {
			if ute, ok := err.(*json.UnmarshalTypeError); ok {
				return NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unmarshal type error: expected=%v, got=%v, field=%v, offset=%v", ute.Type, ute.Value, ute.Field, ute.Offset)).SetInternal(err)
			} else if se, ok := err.(*json.SyntaxError); ok {
				return NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Syntax error: offset=%v, error=%v", se.Offset, se.Error())).SetInternal(err)
			}
			return NewHTTPError(http.StatusBadRequest, err.Error()).SetInternal(err)
		}
	case strings.HasPrefix(ctype, MIMEApplicationXML), strings.HasPrefix(ctype, MIMETextXML):
		if err = xml.NewDecoder(req.Body).Decode(i); err != nil {
			if ute, ok := err.(*xml.UnsupportedTypeError); ok {
				return NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Unsupported type error: type=%v, error=%v", ute.Type, ute.Error())).SetInternal(err)
			} else if se, ok := err.(*xml.SyntaxError); ok {
				return NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Syntax error: line=%v, error=%v", se.Line, se.Error())).SetInternal(err)
			}
			return NewHTTPError(http.StatusBadRequest, err.Error()).SetInternal(err)
		}
	case strings.HasPrefix(ctype, MIMEApplicationForm), strings.HasPrefix(ctype, MIMEMultipartForm):
		params, err := c.FormParams()
		if err != nil {
			return NewHTTPError(http.StatusBadRequest, err.Error()).SetInternal(err)
		}

		// core logic
		if err = b.bindData(i, params, "form"); err != nil {
			return NewHTTPError(http.StatusBadRequest, err.Error()).SetInternal(err)
		}

	default:
		return ErrUnsupportedMediaType
	}
	return
}



func (b *DefaultBinder) bindData(ptr interface{}, data map[string][]string, tag string) error {

	// 获取指针变量的反射对象时，可以通过 reflect.Elem() 方法获取这个指针指向的元素类型。
	// 这个获取过程被称为取元素，等效于对指针类型变量做了一个*操作。



	typ := reflect.TypeOf(ptr).Elem()  	//变量类型


	// reflect.Value 是通过 reflect.ValueOf(x) 获得的，只有当X是指针的时候，才可以通过reflec.Value修改实际变量 x 的值。
	// 由于传入的是指针，需要用 p.Elem() 获取所指向的 v；v.CantSet()输出的是true时便可以用 v.SetFloat() 修改 x 的值。
	val := reflect.ValueOf(ptr).Elem()


	// Kind() 返回 rtype.kind，描述一种基础类型
	if typ.Kind() != reflect.Struct {
		return errors.New("binding element must be a struct")
	}


	for i := 0; i < typ.NumField(); i++ {

		// reflect.StructField: 反射获取结构体字段的元信息，例如：字段名称、Tags 等
		typeField := typ.Field(i)

		// reflect.value: 反射获取&修改字段值
		structField := val.Field(i) //字段值
		if !structField.CanSet() {
			continue
		}

		// 获取 reflect.value 的基础类型（非定义的静态类型）
		structFieldKind := structField.Kind()

		// 获取 reflect.StructField 字段的指定 tag
		inputFieldName := typeField.Tag.Get(tag)
		if inputFieldName == "" {
			// 如果tag为空，就用字段名来表示
			inputFieldName = typeField.Name
			// 如果tag为空，检查该字段是否是嵌套结构体
			if _, ok := bindUnmarshaler(structField); !ok {
				//判断是否是嵌套结构体
				if structFieldKind == reflect.Struct { 
					//递归调用，
					if err := b.bindData(structField.Addr().Interface(), data, tag); err != nil {
						return err
					}
					continue
				}
			}
		}


		inputValue, exists := data[inputFieldName]
		if !exists {
			// Go json.Unmarshal supports case insensitive binding.  However the
			// url params are bound case sensitive which is inconsistent.  To
			// fix this we must check all of the map values in a
			// case-insensitive search.
			inputFieldName = strings.ToLower(inputFieldName)
			for k, v := range data {
				if strings.ToLower(k) == inputFieldName {
					inputValue = v
					exists = true
					break
				}
			}
		}

		if !exists {
			continue
		}


		// Call this first, in case we're dealing with an alias to an array type
		if ok, err := unmarshalField(typeField.Type.Kind(), inputValue[0], structField); ok {
			if err != nil {
				return err
			}
			continue
		}

		numElems := len(inputValue)
		if structFieldKind == reflect.Slice && numElems > 0 {
			//切片元素类型
			sliceOf := structField.Type().Elem().Kind()
			//创建切片
			slice := reflect.MakeSlice(structField.Type(), numElems, numElems)
			//切片逐元素赋值
			for j := 0; j < numElems; j++ {
				if err := setWithProperType(sliceOf, inputValue[j], slice.Index(j)); err != nil {
					return err
				}
			}
			//变量赋值
			val.Field(i).Set(slice)
		} else if _, isTime := structField.Interface().(time.Time); isTime {
			return setTimeField(inputValue, *typeField, *structField)
		} else if err := setWithProperType(typeField.Type.Kind(), inputValue[0], structField); err != nil {
			return err
		}
	}
	return nil
}







//获取 reflect.Kind 对应的golang基础类型关系，以便进行类型转换

func setWithProperType(valueKind reflect.Kind, val string, structField reflect.Value) error {
	

	// But also call it here, in case we're dealing with an array of BindUnmarshalers
	if ok, err := unmarshalField(valueKind, val, structField); ok {
		return err
	}


	switch valueKind {
	case reflect.Ptr:
		//判断 v 是否合法，如果返回 false，那么除了 String() 以外的其他方法调用都会 panic，事前检查是必要的
		if !structField.Elem().IsValid() {
			structField.Set(reflect.New(structField.Type().Elem()))
		}
		//如果值是指针类型，则获取其指向的值类型structField.Elem().Kind()和值对象structField.Elem()，然后执行写入val
		return setWithProperType(structField.Elem().Kind(), val, structField.Elem())

	case reflect.Int:
		return setIntField(val, 0, structField)
	case reflect.Int8:
		return setIntField(val, 8, structField)
	case reflect.Int16:
		return setIntField(val, 16, structField)
	case reflect.Int32:
		return setIntField(val, 32, structField)
	case reflect.Int64:
		return setIntField(val, 64, structField)
	case reflect.Uint:
		return setUintField(val, 0, structField)
	case reflect.Uint8:
		return setUintField(val, 8, structField)
	case reflect.Uint16:
		return setUintField(val, 16, structField)
	case reflect.Uint32:
		return setUintField(val, 32, structField)
	case reflect.Uint64:
		return setUintField(val, 64, structField)
	case reflect.Bool:
		return setBoolField(val, structField)
	case reflect.Float32:
		return setFloatField(val, 32, structField)
	case reflect.Float64:
		return setFloatField(val, 64, structField)
	case reflect.String:
		structField.SetString(val)
	default:
		return errors.New("unknown type")
	}
	return nil
}




func unmarshalField(valueKind reflect.Kind, val string, field reflect.Value) (bool, error) {
	switch valueKind {
	case reflect.Ptr:
		return unmarshalFieldPtr(val, field)
	default:
		return unmarshalFieldNonPtr(val, field)
	}
}

// bindUnmarshaler attempts to unmarshal a reflect.Value into a BindUnmarshaler
func bindUnmarshaler(field reflect.Value) (BindUnmarshaler, bool) {



			
	ptr := reflect.New(field.Type())
	//用CanInterface()来判断是否为可导出字段，读取未导出变量时会panic，
	//panic: reflect.Value.Interface: cannot return value obtained from unexported field or method
	if ptr.CanInterface() {
		// 执行转换，转成interface
		iface := ptr.Interface()
		if unmarshaler, ok := iface.(BindUnmarshaler); ok {
			return unmarshaler, ok
		}
	}

	//不可导出
	return nil, false
}



func unmarshalFieldNonPtr(value string, field reflect.Value) (bool, error) {
	if unmarshaler, ok := bindUnmarshaler(field); ok {
		err := unmarshaler.UnmarshalParam(value)
		field.Set(reflect.ValueOf(unmarshaler).Elem())
		return true, err
	}
	return false, nil
}


func unmarshalFieldPtr(value string, field reflect.Value) (bool, error) {
	if field.IsNil() {
		// Initialize the pointer to a nil value
		field.Set(reflect.New(field.Type().Elem()))
	}
	return unmarshalFieldNonPtr(value, field.Elem())
}



func setIntField(value string, bitSize int, field reflect.Value) error {
	if value == "" {
		value = "0"
	}
	intVal, err := strconv.ParseInt(value, 10, bitSize)
	if err == nil {
		field.SetInt(intVal)
	}
	return err
}

func setUintField(value string, bitSize int, field reflect.Value) error {
	if value == "" {
		value = "0"
	}
	uintVal, err := strconv.ParseUint(value, 10, bitSize)
	if err == nil {
		field.SetUint(uintVal)
	}
	return err
}

func setBoolField(value string, field reflect.Value) error {
	if value == "" {
		value = "false"
	}
	boolVal, err := strconv.ParseBool(value)
	if err == nil {
		field.SetBool(boolVal)
	}
	return err
}

func setFloatField(value string, bitSize int, field reflect.Value) error {
	if value == "" {
		value = "0.0"
	}
	floatVal, err := strconv.ParseFloat(value, bitSize)
	if err == nil {
		field.SetFloat(floatVal)
	}
	return err
}

func setTimeField(value string, structField reflect.StructField, field reflect.Value) error {
	timeFormat := structField.Tag.Get("time_format")
	if timeFormat == "" {
		return errors.New("Blank time format")
	}

	if value == "" {
		field.Set(reflect.ValueOf(time.Time{}))
		return nil
	}


	l := time.Local
	if isUTC, _ := strconv.ParseBool(structField.Tag.Get("time_utc")); isUTC {
		l = time.UTC
	}

	if locTag := structField.Tag.Get("time_location"); locTag != "" {
		loc, err := time.LoadLocation(locTag)
		if err != nil {
			return err
		}
		l = loc
	}

	t, err := time.ParseInLocation(timeFormat, value, l)
	if err != nil {
		return err
	}

	field.Set(reflect.ValueOf(t))
	return nil
}















