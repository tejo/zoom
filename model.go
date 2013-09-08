package zoom

// File contains code strictly related to DefaultData and Model.
// The Register() method and associated methods are also included here.

import (
	"errors"
	"fmt"
	"github.com/stephenalexbrowne/zoom/util"
	"reflect"
)

type DefaultData struct {
	Id string `redis:"-"`
	// TODO: add CreatedAt and UpdatedAt
}

type Model interface {
	GetId() string
	SetId(string)
	// TODO: add getters and setters for CreatedAt and UpdatedAt
}

type modelSpec struct {
	sets       []*externalSet
	lists      []*externalList
	fieldNames []string
}

type externalSet struct {
	redisName string
	fieldName string
}

type externalList struct {
	redisName string
	fieldName string
}

// maps a type to a string identifier. The string is used
// as a key in the redis database.
var typeToName map[reflect.Type]string = make(map[reflect.Type]string)

// maps a string identifier to a type. This is so you can
// pass in a string for the *ById methods
var nameToType map[string]reflect.Type = make(map[string]reflect.Type)

// maps a string identifier to a modelSpec
var modelSpecs map[string]*modelSpec = make(map[string]*modelSpec)

// methods so that DefaultData (and any struct with DefaultData embedded)
// satisifies Model interface
func (d *DefaultData) GetId() string {
	return d.Id
}

func (d *DefaultData) SetId(id string) {
	d.Id = id
}

// adds a model to the map of registered models
// Both name and typeOf(m) must be unique, i.e.
// not already registered
func Register(in interface{}, name string) error {
	typ := reflect.TypeOf(in)

	// make sure the interface is the correct type
	if typ.Kind() != reflect.Ptr {
		return errors.New("zoom: schema must be a pointer to a struct")
	} else if typ.Elem().Kind() != reflect.Struct {
		return errors.New("zoom: schema must be a pointer to a struct")
	}

	// make sure the name and type have not been previously registered
	if alreadyRegisteredType(typ) {
		return NewTypeAlreadyRegisteredError(typ)
	}
	if alreadyRegisteredName(name) {
		return NewNameAlreadyRegisteredError(name)
	}

	// create a new model spec and register its lists and sets
	ms := &modelSpec{}
	if err := compileModelSpec(typ, ms); err != nil {
		return err
	}

	typeToName[typ] = name
	nameToType[name] = typ
	modelSpecs[name] = ms

	return nil
}

func compileModelSpec(typ reflect.Type, ms *modelSpec) error {
	// iterate through fields to find slices and arrays
	elem := typ.Elem()
	numFields := elem.NumField()
	for i := 0; i < numFields; i++ {
		field := elem.Field(i)
		if field.Name != "DefaultData" {
			ms.fieldNames = append(ms.fieldNames, field.Name)
		}
		if util.TypeIsSliceOrArray(field.Type) {
			// we're dealing with a slice or an array, which should be converted to a redis list or set
			tag := field.Tag
			redisName := tag.Get("redis")
			if redisName == "-" {
				continue // skip field
			} else if redisName == "" {
				redisName = field.Name
			}
			redisType := tag.Get("redisType")
			if redisType == "" || redisType == "list" {
				ms.lists = append(ms.lists, &externalList{redisName: redisName, fieldName: field.Name})
			} else if redisType == "set" {
				ms.sets = append(ms.sets, &externalSet{redisName: redisName, fieldName: field.Name})
			} else {
				msg := fmt.Sprintf("zoom: invalid struct tag for redisType: %s. must be either 'set' or 'list'\n", redisType)
				return errors.New(msg)
			}
		}
	}
	return nil
}

func UnregisterName(name string) error {
	typ, ok := nameToType[name]
	if !ok {
		return NewModelNameNotRegisteredError(name)
	}
	delete(nameToType, name)
	delete(typeToName, typ)
	return nil
}

func UnregisterType(typ reflect.Type) error {
	name, ok := typeToName[typ]
	if !ok {
		return NewModelTypeNotRegisteredError(typ)
	}
	delete(nameToType, name)
	delete(typeToName, typ)
	return nil
}

// returns true iff the model name has already been registered
func alreadyRegisteredName(n string) bool {
	_, ok := nameToType[n]
	return ok
}

// returns true iff the model type has already been registered
func alreadyRegisteredType(t reflect.Type) bool {
	_, ok := typeToName[t]
	return ok
}

// get the registered name of the model we're trying to save
// based on the interfaces type. If the interface's name/type has
// not been registered, returns a ModelTypeNotRegisteredError
func getRegisteredNameFromInterface(in interface{}) (string, error) {
	typ := reflect.TypeOf(in)
	name, ok := typeToName[typ]
	if !ok {
		return "", NewModelTypeNotRegisteredError(typ)
	}
	return name, nil
}

// get the registered type of the model we're trying to save
// based on the model name. If the interface's name/type has
// not been registered, returns a ModelNameNotRegisteredError
func getRegisteredTypeFromName(name string) (reflect.Type, error) {
	typ, ok := nameToType[name]
	if !ok {
		return nil, NewModelNameNotRegisteredError(name)
	}
	return typ, nil
}
